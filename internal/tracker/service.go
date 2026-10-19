package tracker

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
)

// Service orchestrates bidirectional sync between the local store and external
// issue trackers (Gongfeng issues + TAPD stories).
type Service struct {
	githubAPIBase         string // Tests inject an HTTP server; production uses api.github.com.
	cfg                   *syncConfigHolder
	gongfeng              *MCPClient
	tapd                  *MCPClient
	authToken             string
	gfSource              string
	tapdSource            string
	mrCache               sync.Map
	landedOnDefault       sync.Map
	logs                  []domain.SyncLog
	logsMu                sync.Mutex
	syncing               bool
	syncMu                sync.Mutex
	autoMu                sync.Mutex
	ticker                *time.Ticker
	stopCh                chan struct{}
	mrTicker              *time.Ticker
	mrStopCh              chan struct{}
	deliveryReconcileOnce sync.Once
	projectAuth           ProjectAuthStore
	OnRequirementImported func(issue domain.Issue)
}

type syncConfigHolder struct {
	mu   sync.RWMutex
	data domain.SyncConfig
}

func (h *syncConfigHolder) Get() domain.SyncConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.data
}

func (h *syncConfigHolder) Set(cfg domain.SyncConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.data = cfg
}

// IssueStoreAdapter abstracts the store operations the sync service needs.
type IssueStoreAdapter interface {
	Issues() []domain.Issue
	Issue(id string) (domain.Issue, bool)
	Repositories() []domain.Repository
	Repository(id string) (domain.Repository, bool)
	Projects() []domain.Project
	Project(id string) (domain.Project, bool)
	PutIssue(v domain.Issue) error
	DeleteIssue(id string) error
	PutSyncConfig(cfg domain.SyncConfig) error
	SyncConfig() domain.SyncConfig
}

// ProjectAuthStore provides the ownership and credential context needed to
// authenticate project-scoped external synchronization.
type ProjectAuthStore interface {
	Projects() []domain.Project
	Project(id string) (domain.Project, bool)
	Repositories() []domain.Repository
	User(id int64) (domain.User, bool)
	MCPServers() []domain.MCPServerDefinition
}

// DeliveryReconcileStore backfills delivery lock fields after external sync.
type DeliveryReconcileStore interface {
	ReconcileMergedMRDeliveries() error
	ReconcileDeliveryMRStates() error
}

func NewService() *Service {
	return &Service{
		cfg: &syncConfigHolder{},
	}
}

// Configure updates the sync config and recreates MCP clients.
// Prefer catalog HTTP MCP Headers (the same assets Agent steps use). Fall back
// to AGENTFLOW_MCP_TOKEN, then ~/.codebuddy/mcp.json OAuth.
func (s *Service) Configure(cfg domain.SyncConfig) {
	s.ConfigureWithCatalog(cfg, nil)
}

// ConfigureWithCatalog binds Gongfeng/TAPD clients from catalog MCP assets.
func (s *Service) ConfigureWithCatalog(cfg domain.SyncConfig, servers []domain.MCPServerDefinition) {
	s.cfg.Set(cfg)
	gf := bindTrackerMCP("gongfeng", cfg, servers, s.authToken, LoadAuthFromCodeBuddy("gongfeng"))
	tapd := bindTrackerMCP("tapd", cfg, servers, s.authToken, LoadAuthFromCodeBuddy("tapd_taihu"))
	s.gfSource, s.tapdSource = gf.Source, tapd.Source
	if gf.URL != "" {
		s.gongfeng = NewMCPClient(gf.URL, gf.Headers)
	} else {
		s.gongfeng = nil
	}
	if tapd.URL != "" {
		s.tapd = NewMCPClient(tapd.URL, tapd.Headers)
	} else {
		s.tapd = nil
	}
}

// ConfigureWithStore enables project-aware authentication. Tracker calls then
// resolve the target project's MCP asset and inject its Owner's Taihu PAT.
func (s *Service) ConfigureWithStore(cfg domain.SyncConfig, store ProjectAuthStore) {
	s.projectAuth = store
	servers := []domain.MCPServerDefinition(nil)
	if store != nil {
		servers = store.MCPServers()
	}
	s.ConfigureWithCatalog(cfg, servers)
}

func (s *Service) trackerClientForProject(kind, projectID string) (*MCPClient, string, error) {
	if s.projectAuth == nil {
		if strings.EqualFold(strings.TrimSpace(kind), "tapd") {
			if s.tapd == nil {
				return nil, s.tapdSource, fmt.Errorf("tapd client not configured")
			}
			return s.tapd, s.tapdSource, nil
		}
		if s.gongfeng == nil {
			return nil, s.gfSource, fmt.Errorf("gongfeng client not configured")
		}
		return s.gongfeng, s.gfSource, nil
	}
	project, ok := s.projectAuth.Project(strings.TrimSpace(projectID))
	if !ok {
		return nil, "none", fmt.Errorf("项目 %s 不存在", projectID)
	}
	if project.OwnerID <= 0 {
		return nil, "none", fmt.Errorf("项目 %s 未设置 Owner", project.ID)
	}
	owner, ok := s.projectAuth.User(project.OwnerID)
	if !ok {
		return nil, "none", fmt.Errorf("项目 %s 的 Owner 不存在", project.ID)
	}
	binding, err := bindProjectOwnerTrackerMCP(kind, s.cfg.Get(), s.projectAuth.MCPServers(), project.ID, owner)
	if err != nil {
		return nil, binding.Source, err
	}
	return NewMCPClient(binding.URL, binding.Headers), binding.Source, nil
}

func (s *Service) trackerClientForWriteBack(kind, projectID string, issue domain.Issue) (*MCPClient, string, error) {
	if s.projectAuth == nil {
		return s.trackerClientForProject(kind, projectID)
	}
	project, ok := s.projectAuth.Project(strings.TrimSpace(projectID))
	if !ok {
		return nil, "none", fmt.Errorf("项目 %s 不存在", projectID)
	}
	if issue.AssigneeUserID > 0 {
		user, ok := s.projectAuth.User(issue.AssigneeUserID)
		if !ok {
			return nil, "none", fmt.Errorf("需求责任人未关联有效用户，无法写回原单")
		}
		binding, err := bindProjectUserTrackerMCP(kind, s.cfg.Get(), s.projectAuth.MCPServers(), project.ID, user, "责任人")
		if err != nil {
			return nil, binding.Source, err
		}
		return NewMCPClient(binding.URL, binding.Headers), binding.Source, nil
	}
	return s.trackerClientForProject(kind, project.ID)
}

func (s *Service) gongfengClientForRemoteProject(remoteProject string) (*MCPClient, string, error) {
	if s.projectAuth != nil {
		remoteProject = strings.TrimSpace(remoteProject)
		for _, repo := range s.projectAuth.Repositories() {
			if strings.TrimSpace(repo.GongfengProjectID) == remoteProject {
				return s.trackerClientForProject("gongfeng", repo.ProjectID)
			}
		}
		return nil, "none", fmt.Errorf("工蜂项目 %s 未绑定本地项目", remoteProject)
	}
	return s.trackerClientForProject("gongfeng", "")
}

// SetAuthToken sets the bearer token used for MCP gateway authentication.
// The token should NOT include the "Bearer " prefix.
func (s *Service) SetAuthToken(token string) {
	s.authToken = token
}

// StartAutoSync launches a background goroutine that periodically pulls.
func (s *Service) StartAutoSync(store IssueStoreAdapter) {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	s.stopAutoSyncLocked()
	cfg := s.cfg.Get()
	if !cfg.AutoSyncEnabled || cfg.AutoSyncIntervalSec <= 0 {
		return
	}
	interval := time.Duration(cfg.AutoSyncIntervalSec) * time.Second
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	s.ticker = time.NewTicker(interval)
	s.stopCh = make(chan struct{})
	ticker, stopCh := s.ticker, s.stopCh
	go func() {
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if err := s.PullAll(store); err != nil {
					log.Printf("auto-sync error: %v", err)
				}
			}
		}
	}()
}

// StopAutoSync halts the background ticker.
func (s *Service) StopAutoSync() {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	s.stopAutoSyncLocked()
}

func (s *Service) stopAutoSyncLocked() {
	if s.ticker != nil {
		s.ticker.Stop()
		s.ticker = nil
	}
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
}

// Logs returns the recent sync log entries (most recent first).
func (s *Service) Logs() []domain.SyncLog {
	s.logsMu.Lock()
	defer s.logsMu.Unlock()
	out := make([]domain.SyncLog, 0, len(s.logs))
	out = append(out, s.logs...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

func (s *Service) addLog(l domain.SyncLog) {
	s.logsMu.Lock()
	defer s.logsMu.Unlock()
	s.logs = append(s.logs, l)
	if len(s.logs) > 200 {
		s.logs = s.logs[len(s.logs)-200:]
	}
}

func (s *Service) beginSync() bool {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if s.syncing {
		return false
	}
	s.syncing = true
	return true
}

func (s *Service) endSync() {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	s.syncing = false
}

func (s *Service) IsSyncing() bool {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	return s.syncing
}

// PullAll pulls both Gongfeng issues and TAPD stories into the store.
func (s *Service) PullAll(store IssueStoreAdapter) error {
	if !s.beginSync() {
		return fmt.Errorf("sync already in progress")
	}
	defer s.endSync()

	cfg := s.cfg.Get()
	var lastErr error
	syncCount := 0

	repositories := store.Repositories()
	for _, repo := range repositories {
		if repo.Provider == "github" {
			started := time.Now().UTC()
			n, err := s.pullGitHubIssues(store, repo)
			status, message := "success", fmt.Sprintf("GitHub: imported %d work items", n)
			if err != nil {
				status = "failed"
				message = err.Error()
			}
			s.addLog(domain.SyncLog{ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "github", Action: "sync_issues", Status: status, Message: message, ProjectID: repo.ProjectID, StartedAt: started, FinishedAt: time.Now().UTC()})
			syncCount += n
			if err != nil {
				lastErr = err
			}
			continue
		}
		if repo.Provider != "gongfeng" || repo.GongfengProjectID == "" {
			continue
		}
		client, source, err := s.trackerClientForProject("gongfeng", repo.ProjectID)
		if err != nil {
			lastErr = err
			continue
		}
		n, err := s.pullGongfengIssues(client, store, repo)
		if err != nil {
			lastErr = fmt.Errorf("%s", s.authError("工蜂", source, err))
		}
		syncCount += n
	}

	n, err := s.pullTAPDStories(store)
	if err != nil {
		if lastErr == nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("%v；%v", lastErr, err)
		}
	}
	syncCount += n

	pruneDuplicateExternalIssues(store)
	if rs, ok := store.(DeliveryReconcileStore); ok {
		if err := rs.ReconcileMergedMRDeliveries(); err != nil && lastErr == nil {
			lastErr = fmt.Errorf("delivery reconcile: %w", err)
		}
		_ = rs.ReconcileDeliveryMRStates()
	}

	cfg = s.cfg.Get()
	cfg.LastSyncAt = time.Now().UTC()
	if lastErr != nil {
		cfg.LastSyncStatus = "failed"
		cfg.LastSyncError = lastErr.Error()
	} else {
		cfg.LastSyncStatus = "success"
		cfg.LastSyncError = ""
	}
	s.cfg.Set(cfg)
	_ = store.PutSyncConfig(cfg)

	return lastErr
}

func (s *Service) PullScheduleJob(store IssueStoreAdapter, job domain.ScheduleJob) (int, []string, error) {
	if !s.beginSync() {
		return 0, nil, fmt.Errorf("sync already in progress")
	}
	defer s.endSync()
	switch strings.ToLower(strings.TrimSpace(job.Source)) {
	case "github":
		count := 0
		var last error
		for _, repo := range store.Repositories() {
			if repo.Provider == "github" && (job.ProjectID == "" || repo.ProjectID == job.ProjectID) {
				n, err := s.pullGitHubIssues(store, repo)
				count += n
				if err != nil {
					last = err
				}
			}
		}
		return count, nil, last
	case "gongfeng":
		count := 0
		var last error
		for _, repo := range store.Repositories() {
			if job.ProjectID != "" && repo.ProjectID != job.ProjectID {
				continue
			}
			if repo.Provider != "gongfeng" || repo.GongfengProjectID == "" {
				continue
			}
			client, _, err := s.trackerClientForProject("gongfeng", repo.ProjectID)
			if err != nil {
				last = err
				continue
			}
			n, err := s.pullGongfengIssues(client, store, repo)
			count += n
			if err != nil {
				last = err
			}
		}
		return count, nil, last
	case "tapd":
		if strings.TrimSpace(job.TAPDWorkspaceID) == "" || strings.TrimSpace(job.TAPDIterationID) == "" {
			return 0, nil, fmt.Errorf("schedule job needs TAPD workspace and iteration")
		}
		return s.pullScheduledTAPDPool(store, job)
	default:
		return 0, nil, fmt.Errorf("unknown schedule source %q", job.Source)
	}
}

// --- Gongfeng Issues ---

type gongfengIssue struct {
	ID          int    `json:"id"`
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       string `json:"state"`
	Priority    *int   `json:"priority"`
	Author      struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"author"`
	Assignee *struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"assignee"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Service) pullGongfengIssues(client *MCPClient, store IssueStoreAdapter, repo domain.Repository) (int, error) {
	started := time.Now().UTC()
	issuesByIID := map[int]gongfengIssue{}
	for _, state := range []string{"opened", "closed"} {
		for page := 1; page <= 20; page++ {
			args := map[string]any{"project_id": repo.GongfengProjectID, "per_page": 100, "page": page, "state": state, "order_by": "updated_at", "sort": "desc"}
			raw, err := client.CallTool("search_project_issues", args)
			if err != nil {
				s.addLog(domain.SyncLog{ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "gongfeng", Action: "sync_issues", Status: "failed", Message: actionableAuthError("工蜂", err), ProjectID: repo.ProjectID, StartedAt: started, FinishedAt: time.Now().UTC()})
				return 0, err
			}
			pageIssues, err := parseGongfengIssues(raw)
			if err != nil {
				return 0, err
			}
			for _, issue := range pageIssues {
				issuesByIID[issue.IID] = issue
			}
			if len(pageIssues) < 100 {
				break
			}
		}
	}
	issues := make([]gongfengIssue, 0, len(issuesByIID))
	for _, issue := range issuesByIID {
		issues = append(issues, issue)
	}
	if len(issues) == 0 {
		s.addLog(domain.SyncLog{
			ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "gongfeng",
			Action: "sync_issues", Status: "success", Message: "工蜂未返回工作项",
			ProjectID: repo.ProjectID,
			StartedAt: started, FinishedAt: time.Now().UTC(),
		})
		return 0, nil
	}

	count := 0
	for _, gi := range issues {
		existing, found := findGongfengIssue(store, repo, gi)
		issueID := fmt.Sprintf("gf-%s-%d", repo.ID, gi.IID)
		if found {
			issueID = existing.ID
		}
		issue := domain.Issue{
			ID:           issueID,
			ProjectID:    repo.ProjectID,
			RepositoryID: repo.ID,
			Kind:         "issue",
			Title:        gi.Title,
			Description:  gi.Description,
			Priority:     gongfengPriority(gi.Priority),
			Status:       gongfengStateToStatus(gi.State),
			CloseSource:  closeSourceForStatus(gongfengStateToStatus(gi.State)),
			Source:       "gongfeng",
			ExternalRef: &domain.ExternalRef{
				Source:        "gongfeng",
				ExternalID:    fmt.Sprintf("%d", gi.ID),
				ExternalIID:   gi.IID,
				ExternalURL:   strings.TrimSuffix(repo.WebURL, "/") + fmt.Sprintf("/issues/%d", gi.IID),
				ExternalState: gi.State,
				ExternalType:  "issue",
				SyncedAt:      time.Now().UTC(),
			},
		}
		if found {
			applyExistingIssue(&issue, existing)
		} else {
			issue.Number = gi.IID
			issue.CreatedAt = parseTime(gi.CreatedAt)
		}
		issue.UpdatedAt = time.Now().UTC()
		if err := store.PutIssue(issue); err != nil {
			log.Printf("sync gongfeng issue %d: %v", gi.IID, err)
			continue
		}
		count++
	}

	s.addLog(domain.SyncLog{
		ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "gongfeng",
		Action: "sync_issues", Status: "success", Count: count,
		Message:   fmt.Sprintf("同步 %d 条工蜂 issue", count),
		ProjectID: repo.ProjectID,
		StartedAt: started, FinishedAt: time.Now().UTC(),
	})
	return count, nil
}

func parseGongfengIssues(raw string) ([]gongfengIssue, error) {
	var direct []gongfengIssue
	if err := json.Unmarshal([]byte(raw), &direct); err == nil {
		return direct, nil
	}
	var wrapped struct {
		Data   []gongfengIssue `json:"data"`
		Issues []gongfengIssue `json:"issues"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err == nil {
		if wrapped.Data != nil {
			return wrapped.Data, nil
		}
		if wrapped.Issues != nil {
			return wrapped.Issues, nil
		}
	}
	return nil, fmt.Errorf("could not parse Gongfeng issues response")
}

// CreateGongfengIssue pushes a local issue to Gongfeng, creating a new issue.
func (s *Service) CreateGongfengIssue(store IssueStoreAdapter, issue domain.Issue) (domain.Issue, error) {
	started := time.Now().UTC()
	repo, ok := store.Repository(issue.RepositoryID)
	if !ok || repo.Provider != "gongfeng" || repo.GongfengProjectID == "" {
		return issue, fmt.Errorf("issue must bind a valid gongfeng repository")
	}
	client, _, err := s.trackerClientForProject("gongfeng", repo.ProjectID)
	if err != nil {
		return issue, err
	}
	args := map[string]any{
		"project_id":  repo.GongfengProjectID,
		"title":       issue.Title,
		"description": issue.Description,
	}
	if issue.Priority != "" {
		args["labels"] = "priority-" + issue.Priority
	}
	raw, err := client.CallTool("create_issue", args)
	if err != nil {
		s.addLog(domain.SyncLog{
			ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "push", Source: "gongfeng",
			Action: "create_issue", Status: "failed", Message: actionableAuthError("工蜂", err),
			StartedAt: started, FinishedAt: time.Now().UTC(),
		})
		return issue, err
	}

	var gi gongfengIssue
	if err := json.Unmarshal([]byte(raw), &gi); err != nil {
		var arr []gongfengIssue
		if err2 := json.Unmarshal([]byte(raw), &arr); err2 == nil && len(arr) > 0 {
			gi = arr[0]
		} else {
			return issue, fmt.Errorf("decode created issue: %w", err)
		}
	}

	issue.Source = "gongfeng"
	issue.ExternalRef = &domain.ExternalRef{
		Source:        "gongfeng",
		ExternalID:    fmt.Sprintf("%d", gi.ID),
		ExternalIID:   gi.IID,
		ExternalURL:   strings.TrimSuffix(repo.WebURL, "/") + fmt.Sprintf("/issues/%d", gi.IID),
		ExternalState: gi.State,
		ExternalType:  "issue",
		SyncedAt:      time.Now().UTC(),
	}
	if gi.IID > 0 {
		issue.Number = gi.IID
		stableID := fmt.Sprintf("gf-%s-%d", repo.ID, gi.IID)
		oldID := issue.ID
		issue.ID = stableID
		if oldID != "" && oldID != stableID && !strings.HasPrefix(oldID, "gf-") {
			_ = store.DeleteIssue(oldID)
		}
	}
	_ = store.PutIssue(issue)

	s.addLog(domain.SyncLog{
		ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "push", Source: "gongfeng",
		Action: "create_issue", Status: "success", Count: 1,
		Message:   fmt.Sprintf("创建工蜂 issue #%d: %s", gi.IID, gi.Title),
		ProjectID: issue.ProjectID,
		StartedAt: started, FinishedAt: time.Now().UTC(),
	})
	return issue, nil
}

// WriteBackClarification appends a clarification summary to the linked external
// work item. It intentionally uses comments rather than replacing the original
// description, so subsequent pulls cannot erase the clarification.
func (s *Service) WriteBackClarification(store IssueStoreAdapter, issue domain.Issue, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("clarification summary is empty")
	}
	if issue.ExternalRef == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(issue.Source)) {
	case "gongfeng":
		repo, ok := store.Repository(issue.RepositoryID)
		if !ok || strings.TrimSpace(repo.GongfengProjectID) == "" {
			return fmt.Errorf("issue must bind a valid gongfeng repository")
		}
		issueID, err := strconv.ParseInt(strings.TrimSpace(issue.ExternalRef.ExternalID), 10, 64)
		if err != nil || issueID <= 0 {
			return fmt.Errorf("gongfeng external issue id is invalid")
		}
		client, _, err := s.trackerClientForWriteBack("gongfeng", repo.ProjectID, issue)
		if err != nil {
			return err
		}
		_, err = client.CallTool("create_issue_note", map[string]any{
			"project_id":   repo.GongfengProjectID,
			"issue_id":     issueID,
			"note_message": body,
		})
		return err
	case "tapd":
		project, ok := store.Project(issue.ProjectID)
		if !ok {
			return fmt.Errorf("project %q does not exist", issue.ProjectID)
		}
		workspaceID := tapdWorkspaceFromURL(issue.ExternalRef.ExternalURL)
		if workspaceID == "" {
			workspaceID = strings.TrimSpace(project.TAPDWorkspaceID)
		}
		if workspaceID == "" {
			return fmt.Errorf("project %q needs tapdWorkspaceId configured", issue.ProjectID)
		}
		entryID := strings.TrimSpace(issue.ExternalRef.ExternalID)
		if entryID == "" {
			return fmt.Errorf("tapd external work item id is missing")
		}
		client, _, err := s.trackerClientForWriteBack("tapd", project.ID, issue)
		if err != nil {
			return err
		}
		_, err = client.CallTool("proxy_execute_tool", map[string]any{
			"tool_name": "comments_create",
			"tool_args": map[string]any{
				"workspace_id": workspaceID,
				"entry_type":   tapdExternalType(issue),
				"entry_id":     entryID,
				"description":  body,
			},
		})
		return err
	default:
		return nil
	}
}

// --- TAPD Stories ---

type tapdStory struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Status        string `json:"status"`
	Priority      string `json:"priority"`
	PriorityLabel string `json:"priority_label"`
	StoryID       string `json:"story_id"`
	Owner         string `json:"owner"`
	Creator       string `json:"creator"`
	IterationID   string `json:"iteration_id"`
	Created       string `json:"created"`
	Modified      string `json:"modified"`
	// Bug-only fields: TAPD bugs carry their title in "title" (mapped into
	// Name by collectTAPDStories) and their handler in "current_owner".
	CurrentOwner string `json:"current_owner"`
	Severity     string `json:"severity"`
}

// Fields requested from the TAPD MCP gateway. Without an explicit "fields"
// argument stories_get / bugs_get return a trimmed record set that omits
// "description" (and for stories, owner/priority), which silently produced
// empty issue bodies. Note: TAPD prunes strictly to the requested list, so
// "name"/"title" must be included or collectTAPDStories cannot match records.
const (
	// creator is required: a story's platform assignee is the TAPD creator,
	// not the handler in owner. TAPD omits any field that is not requested.
	tapdStoryFields = "id,name,description,status,owner,creator,priority,iteration_id,created,modified"
	tapdBugFields   = "id,title,description,status,current_owner,severity,priority,iteration_id,created,modified"
	tapdTaskFields  = "id,name,description,status,owner,creator,priority_label,story_id,iteration_id,created,modified"
)

// tapdPool identifies one TAPD requirement pool (workspace + iteration) and the
// local project that owns issues pulled from it.
type tapdPool struct {
	ProjectID   string
	WorkspaceID string
	IterationID string
}

// tapdPools collects the set of TAPD pools to sync: one per project that has
// workspace+iteration configured on the project row.
func (s *Service) tapdPools(store IssueStoreAdapter) []tapdPool {
	var pools []tapdPool
	seenKey := map[string]bool{}
	add := func(projectID, ws, it string) {
		ws = strings.TrimSpace(ws)
		it = strings.TrimSpace(it)
		if ws == "" || it == "" {
			return
		}
		key := ws + "|" + it
		if seenKey[key] {
			return
		}
		seenKey[key] = true
		pools = append(pools, tapdPool{ProjectID: projectID, WorkspaceID: ws, IterationID: it})
	}
	for _, p := range store.Projects() {
		add(p.ID, p.TAPDWorkspaceID, p.TAPDIterationID)
	}
	return pools
}

func (s *Service) pullTAPDStories(store IssueStoreAdapter) (int, error) {
	started := time.Now().UTC()
	pools := s.tapdPools(store)
	if len(pools) == 0 {
		// No TAPD pool configured anywhere; do not reconcile (would delete all).
		return 0, nil
	}
	seen := map[string]bool{}
	total := 0
	var lastErr error
	for _, pool := range pools {
		client, source, err := s.trackerClientForProject("tapd", pool.ProjectID)
		if err != nil {
			lastErr = err
			s.addLog(domain.SyncLog{
				ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd",
				Action: "sync_stories", Status: "failed", Count: 0,
				Message:   actionableAuthError("TAPD", err),
				ProjectID: pool.ProjectID,
				StartedAt: started, FinishedAt: time.Now().UTC(),
			})
			continue
		}
		n, err := s.pullTAPDPool(client, store, pool, seen)
		total += n
		if err != nil {
			// Log each failing pool immediately with its project owner so the
			// failure is only visible in that project's sync panel (an
			// unscoped aggregate would leak one project's misconfiguration
			// into every other project's diagnostics).
			lastErr = fmt.Errorf("项目 %s：%s", pool.ProjectID, s.authError("TAPD", source, err))
			s.addLog(domain.SyncLog{
				ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd",
				Action: "sync_stories", Status: "failed", Count: n,
				Message:   actionableAuthError("TAPD", err),
				ProjectID: pool.ProjectID,
				StartedAt: started, FinishedAt: time.Now().UTC(),
			})
		}
	}

	// Reconcile once across all pools: a local TAPD issue is stale only if it
	// is absent from every pool we successfully queried.
	removed := 0
	if lastErr == nil {
		removed = reconcileTAPDIssues(store, seen)
	}

	if lastErr != nil {
		// Pool-level failures are already logged above with their project
		// owner; keep the platform-level status propagation without an
		// unscoped diagnostics entry.
		return total, lastErr
	}

	s.addLog(domain.SyncLog{
		ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd",
		Action: "sync_stories", Status: "success", Count: total,
		Message:   fmt.Sprintf("同步 %d 个需求池：拉取 %d 条（需求+缺陷），清理 %d 条", len(pools), total, removed),
		StartedAt: started, FinishedAt: time.Now().UTC(),
	})
	return total, nil
}

// pullScheduledTAPDPool is intentionally non-destructive: a job owns one
// iteration, so it must not reconcile issues from other project pools.
func (s *Service) pullScheduledTAPDPool(store IssueStoreAdapter, job domain.ScheduleJob) (int, []string, error) {
	started := time.Now().UTC()
	projectID := strings.TrimSpace(job.ProjectID)
	if projectID == "" {
		projectID = "platform"
	}
	pool := tapdPool{ProjectID: projectID, WorkspaceID: strings.TrimSpace(job.TAPDWorkspaceID), IterationID: strings.TrimSpace(job.TAPDIterationID)}
	seen := map[string]bool{}
	client, _, err := s.trackerClientForProject("tapd", projectID)
	if err != nil {
		return 0, []string{}, err
	}
	count, err := s.pullTAPDPool(client, store, pool, seen)
	status, message := "success", tapdPullMessage(pool.IterationID, count, 0)
	if err != nil {
		status, message = "failed", actionableAuthError("TAPD", err)
	}
	s.addLog(domain.SyncLog{ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd", Action: "sync_stories", Status: status, Count: count, Message: message, ProjectID: projectID, StartedAt: started, FinishedAt: time.Now().UTC()})
	return count, scheduledTAPDIssueIDs(store, projectID, seen), err
}

func scheduledTAPDIssueIDs(store IssueStoreAdapter, projectID string, seen map[string]bool) []string {
	issueIDs := make([]string, 0, len(seen))
	for storyID := range seen {
		if issue, ok := findTAPDIssue(store, storyID); ok && issue.ProjectID == projectID {
			issueIDs = append(issueIDs, issue.ID)
		}
	}
	sort.Strings(issueIDs)
	return issueIDs
}

// pullTAPDPool pulls stories and bugs from a single pool, writing them into
// the store under pool.ProjectID, and records their TAPD IDs into the shared
// seen map.
func (s *Service) pullTAPDPool(client *MCPClient, store IssueStoreAdapter, pool tapdPool, seen map[string]bool) (int, error) {
	const pageSize = 50
	var stories []tapdStory
	for page := 1; page <= 40; page++ {
		args := map[string]any{
			"tool_name": "stories_get",
			"tool_args": map[string]any{
				"workspace_id": pool.WorkspaceID,
				"iteration_id": pool.IterationID,
				"limit":        pageSize,
				"page":         page,
				"fields":       tapdStoryFields,
			},
		}
		raw, err := client.CallTool("proxy_execute_tool", args)
		if err != nil {
			return 0, err
		}
		pageStories, err := parseTAPDStories(raw)
		if err != nil {
			return 0, err
		}
		stories = append(stories, pageStories...)
		if len(pageStories) < pageSize {
			break
		}
	}

	count := 0
	for _, st := range stories {
		st.ID = strings.TrimSpace(st.ID)
		if st.ID == "" || seen[st.ID] {
			continue
		}
		seen[st.ID] = true
		if _, err := s.upsertTAPDStory(store, pool.ProjectID, pool.WorkspaceID, st, 0, false); err != nil {
			log.Printf("sync tapd story %s: %v", st.ID, err)
			continue
		}
		count++
	}

	bugCount, err := s.pullTAPDBugs(client, store, pool, seen, pageSize)
	if err != nil {
		return count, err
	}
	return count + bugCount, nil
}

// pullTAPDBugs pulls bug tickets for a pool's workspace+iteration and upserts
// them as kind=bug issues, mirroring the story pull path.
func (s *Service) pullTAPDBugs(client *MCPClient, store IssueStoreAdapter, pool tapdPool, seen map[string]bool, pageSize int) (int, error) {
	var bugs []tapdStory
	for page := 1; page <= 40; page++ {
		args := map[string]any{
			"tool_name": "bugs_get",
			"tool_args": map[string]any{
				"workspace_id": pool.WorkspaceID,
				"iteration_id": pool.IterationID,
				"limit":        pageSize,
				"page":         page,
				"fields":       tapdBugFields,
			},
		}
		raw, err := client.CallTool("proxy_execute_tool", args)
		if err != nil {
			// TAPD returns empty result sets as an is_error tool response;
			// an iteration without bugs is a normal state, not a failure.
			if isTAPDEmptyError(err) {
				break
			}
			return 0, err
		}
		pageBugs, err := parseTAPDBugs(raw)
		if err != nil {
			return 0, err
		}
		bugs = append(bugs, pageBugs...)
		if len(pageBugs) < pageSize {
			break
		}
	}

	count := 0
	for _, bg := range bugs {
		bg.ID = strings.TrimSpace(bg.ID)
		if bg.ID == "" || seen[bg.ID] {
			continue
		}
		seen[bg.ID] = true
		if _, err := s.upsertTAPDBug(store, pool.ProjectID, pool.WorkspaceID, bg, 0, false); err != nil {
			log.Printf("sync tapd bug %s: %v", bg.ID, err)
			continue
		}
		count++
	}
	return count, nil
}

// ImportTAPDStory fetches one TAPD story, bug or task by pasted web URL and upserts it into
// the given AgentFlow project. The story does not need to belong to the
// project's configured iteration pool. fallbackUserID is used as the issue
// responsible user when the TAPD creator cannot be matched to a platform user.
func (s *Service) ImportTAPDStory(store IssueStoreAdapter, projectID, rawURL string, fallbackUserID int64) (domain.Issue, error) {
	started := time.Now().UTC()
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return domain.Issue{}, fmt.Errorf("projectId is required")
	}
	if _, ok := store.Project(projectID); !ok {
		return domain.Issue{}, fmt.Errorf("项目 %s 不存在", projectID)
	}
	ref, err := ParseTAPDStoryURL(rawURL)
	if err != nil {
		return domain.Issue{}, err
	}
	client, _, err := s.trackerClientForProject("tapd", projectID)
	if err != nil {
		s.addLog(domain.SyncLog{ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd", Action: "import_story", Status: "failed", Message: actionableAuthError("TAPD", err), ProjectID: projectID, StartedAt: started, FinishedAt: time.Now().UTC()})
		return domain.Issue{}, err
	}
	toolName, fields := tapdReadTool(ref.Kind)
	raw, err := client.CallTool("proxy_execute_tool", map[string]any{
		"tool_name": toolName,
		"tool_args": map[string]any{
			"workspace_id": ref.WorkspaceID,
			"id":           ref.StoryID,
			"limit":        1,
			"page":         1,
			"fields":       fields,
		},
	})
	if err != nil {
		s.addLog(domain.SyncLog{ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd", Action: "import_story", Status: "failed", Message: actionableAuthError("TAPD", err), ProjectID: projectID, StartedAt: started, FinishedAt: time.Now().UTC()})
		return domain.Issue{}, err
	}
	records, err := parseTAPDRecords(raw, ref.Kind+"s")
	if err != nil {
		return domain.Issue{}, err
	}
	st, ok := pickTAPDStory(records, ref.StoryID)
	if !ok {
		return domain.Issue{}, fmt.Errorf("未找到 TAPD %s %s", tapdRecordLabel(ref.Kind), ref.StoryID)
	}
	var issue domain.Issue
	if ref.Kind == "task" {
		issue, err = s.upsertTAPDTask(store, projectID, ref.WorkspaceID, st, fallbackUserID, true)
	} else if ref.Kind == "bug" {
		issue, err = s.upsertTAPDBug(store, projectID, ref.WorkspaceID, st, fallbackUserID, true)
	} else {
		issue, err = s.upsertTAPDStory(store, projectID, ref.WorkspaceID, st, fallbackUserID, true)
	}
	if err != nil {
		return domain.Issue{}, err
	}
	recordLabel := tapdRecordLabel(ref.Kind)
	s.addLog(domain.SyncLog{ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd", Action: "import_story", Status: "success", Count: 1, Message: fmt.Sprintf("导入 TAPD %s %s: %s", recordLabel, st.ID, st.Name), ProjectID: projectID, StartedAt: started, FinishedAt: time.Now().UTC()})
	return issue, nil
}

// ResyncTAPDIssue re-pulls one TAPD story/bug/task identified by the issue's
// external reference and refreshes the local copy (title/description/status/
// priority). Local assignee, clarification state and repository bindings are
// preserved by applyExistingIssue; a manual resync never re-enters the
// clarification gate.
func (s *Service) ResyncTAPDIssue(store IssueStoreAdapter, issue domain.Issue) (domain.Issue, error) {
	started := time.Now().UTC()
	if strings.TrimSpace(issue.ProjectID) == "" {
		return domain.Issue{}, fmt.Errorf("projectId is required")
	}
	ref := issue.ExternalRef
	if issue.Source != "tapd" || ref == nil || strings.TrimSpace(ref.ExternalID) == "" {
		return domain.Issue{}, fmt.Errorf("仅 TAPD 来源的需求支持重新同步")
	}
	kind := tapdExternalType(issue)
	workspaceID := tapdWorkspaceFromURL(ref.ExternalURL)
	if workspaceID == "" {
		if project, ok := store.Project(issue.ProjectID); ok {
			workspaceID = strings.TrimSpace(project.TAPDWorkspaceID)
		}
	}
	if workspaceID == "" {
		return domain.Issue{}, fmt.Errorf("无法确定 TAPD 项目 ID，请先在项目设置中配置 TAPD 工作空间")
	}
	client, _, err := s.trackerClientForProject("tapd", issue.ProjectID)
	if err != nil {
		s.addLog(domain.SyncLog{ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd", Action: "resync", Status: "failed", Message: actionableAuthError("TAPD", err), ProjectID: issue.ProjectID, StartedAt: started, FinishedAt: time.Now().UTC()})
		return domain.Issue{}, err
	}
	toolName, fields := tapdReadTool(kind)
	raw, err := client.CallTool("proxy_execute_tool", map[string]any{
		"tool_name": toolName,
		"tool_args": map[string]any{
			"workspace_id": workspaceID,
			"id":           ref.ExternalID,
			"limit":        1,
			"page":         1,
			"fields":       fields,
		},
	})
	if err != nil {
		s.addLog(domain.SyncLog{ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd", Action: "resync", Status: "failed", Message: actionableAuthError("TAPD", err), ProjectID: issue.ProjectID, StartedAt: started, FinishedAt: time.Now().UTC()})
		return domain.Issue{}, err
	}
	records, err := parseTAPDRecords(raw, kind+"s")
	if err != nil {
		return domain.Issue{}, err
	}
	st, ok := pickTAPDStory(records, ref.ExternalID)
	if !ok {
		return domain.Issue{}, fmt.Errorf("未找到 TAPD %s %s", tapdRecordLabel(kind), ref.ExternalID)
	}
	var updated domain.Issue
	if kind == "task" {
		updated, err = s.upsertTAPDTask(store, issue.ProjectID, workspaceID, st, 0, false)
	} else if kind == "bug" {
		updated, err = s.upsertTAPDBug(store, issue.ProjectID, workspaceID, st, 0, false)
	} else {
		updated, err = s.upsertTAPDStory(store, issue.ProjectID, workspaceID, st, 0, false)
	}
	if err != nil {
		return domain.Issue{}, err
	}
	recordLabel := tapdRecordLabel(kind)
	s.addLog(domain.SyncLog{ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "pull", Source: "tapd", Action: "resync", Status: "success", Count: 1, Message: fmt.Sprintf("重新同步 TAPD %s %s: %s", recordLabel, st.ID, st.Name), ProjectID: issue.ProjectID, StartedAt: started, FinishedAt: time.Now().UTC()})
	return updated, nil
}

func tapdReadTool(kind string) (string, string) {
	switch kind {
	case "bug":
		return "bugs_get", tapdBugFields
	case "task":
		return "tasks_get", tapdTaskFields
	default:
		return "stories_get", tapdStoryFields
	}
}

func tapdRecordLabel(kind string) string {
	switch kind {
	case "bug":
		return "缺陷"
	case "task":
		return "任务"
	default:
		return "需求"
	}
}

func pickTAPDStory(stories []tapdStory, storyID string) (tapdStory, bool) {
	storyID = strings.TrimSpace(storyID)
	for _, st := range stories {
		if strings.TrimSpace(st.ID) == storyID {
			return st, true
		}
	}
	return tapdStory{}, false
}

func (s *Service) upsertTAPDStory(store IssueStoreAdapter, projectID, workspaceID string, st tapdStory, fallbackUserID int64, gateClarification bool) (domain.Issue, error) {
	return s.upsertTAPDRecord(store, projectID, workspaceID, st, "requirement", fallbackUserID, gateClarification)
}

// upsertTAPDBug stores a TAPD bug ticket as a kind=bug issue. Bugs follow the
// same upsert rules as stories: sync never clobbers local assignee or
// clarification state (see applyExistingIssue).
func (s *Service) upsertTAPDBug(store IssueStoreAdapter, projectID, workspaceID string, bg tapdStory, fallbackUserID int64, gateClarification bool) (domain.Issue, error) {
	return s.upsertTAPDRecord(store, projectID, workspaceID, bg, "bug", fallbackUserID, gateClarification)
}

// TAPD tasks share the requirement intake and execution flow locally. Keep
// ExternalType=task so refreshes and comments still target the TAPD task.
func (s *Service) upsertTAPDTask(store IssueStoreAdapter, projectID, workspaceID string, task tapdStory, fallbackUserID int64, gateClarification bool) (domain.Issue, error) {
	return s.upsertTAPDRecord(store, projectID, workspaceID, task, "task", fallbackUserID, gateClarification)
}

func (s *Service) upsertTAPDRecord(store IssueStoreAdapter, projectID, workspaceID string, st tapdStory, kind string, fallbackUserID int64, gateClarification bool) (domain.Issue, error) {
	st.ID = strings.TrimSpace(st.ID)
	if st.ID == "" {
		return domain.Issue{}, fmt.Errorf("TAPD 需求缺少 ID")
	}
	externalType := "story"
	issueKind := kind
	if kind == "bug" || kind == "task" {
		externalType = kind
	}
	if kind == "task" {
		issueKind = "requirement"
	}
	// Stories: the person who created the TAPD ticket is the platform assignee.
	// Bugs have no equivalent creator field here; their handler is current_owner.
	assignee := st.Creator
	if kind == "bug" {
		assignee = st.CurrentOwner
	}
	status := tapdStatusToStatus(st.Status)
	priority := tapdPriority(st.Priority)
	if kind == "bug" {
		status = tapdBugStatusToStatus(st.Status)
	} else if kind == "task" {
		status = tapdTaskStatusToStatus(st.Status)
		priority = tapdTaskPriority(st.PriorityLabel)
	}
	existing, found := findTAPDRecord(store, st.ID, externalType, workspaceID)
	issueID := fmt.Sprintf("tapd-%s", st.ID)
	if externalType == "task" {
		issueID = "tapd-task-" + st.ID
	}
	if found {
		issueID = existing.ID
	} else if _, occupied := store.Issue(issueID); occupied {
		// Preserve historic IDs while allowing another workspace or record
		// type to use the same numeric TAPD ID without overwriting it.
		issueID = fmt.Sprintf("tapd-%s-%s-%s", workspaceID, externalType, st.ID)
		if _, occupied := store.Issue(issueID); occupied {
			return domain.Issue{}, fmt.Errorf("TAPD 工作项 %s 的本地 ID 已被占用", st.ID)
		}
	}
	if workspaceID == "" && found && existing.ExternalRef != nil {
		workspaceID = tapdWorkspaceFromURL(existing.ExternalRef.ExternalURL)
	}
	issue := domain.Issue{
		ID:          issueID,
		ProjectID:   projectID,
		Title:       st.Name,
		Description: st.Description,
		Priority:    priority,
		Status:      status,
		CloseSource: closeSourceForStatus(status),
		Kind:        issueKind,
		Source:      "tapd",
		ExternalRef: &domain.ExternalRef{
			Source:        "tapd",
			ExternalID:    st.ID,
			ExternalURL:   fmt.Sprintf("https://tapd.example.com/tapd_fe/%s/%s/detail/%s", workspaceID, externalType, st.ID),
			ExternalState: st.Status,
			ExternalType:  externalType,
			SyncedAt:      time.Now().UTC(),
			Imported:      gateClarification,
		},
	}
	if found {
		applyExistingIssue(&issue, existing)
		if issue.AssigneeUserID == 0 {
			issue.AssigneeUserID = s.resolveTAPDAssignee(store, projectID, assignee, fallbackUserID)
		}
	} else {
		issue.CreatedAt = parseTime(st.Created)
		issue.AssigneeUserID = s.resolveTAPDAssignee(store, projectID, assignee, fallbackUserID)
	}
	if gateClarification {
		scoreStoredRequirement(store, &issue)
	}
	issue.UpdatedAt = time.Now().UTC()
	if err := store.PutIssue(issue); err != nil {
		return domain.Issue{}, err
	}
	stored, ok := store.Issue(issue.ID)
	if ok {
		return stored, nil
	}
	return issue, nil
}

type tapdUserDirectory interface {
	Users() []domain.User
	UserByLoginName(string) (domain.User, bool)
}

func (s *Service) tapdUserDirectory(store IssueStoreAdapter) tapdUserDirectory {
	if dir, ok := store.(tapdUserDirectory); ok {
		return dir
	}
	if dir, ok := s.projectAuth.(tapdUserDirectory); ok {
		return dir
	}
	return nil
}

func firstTAPDOwner(raw string) string {
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part != "" {
			return part
		}
	}
	return ""
}

func tapdOwnerMatchNames(raw string) []string {
	owner := firstTAPDOwner(raw)
	if owner == "" {
		return nil
	}
	names := []string{owner}
	if i := strings.LastIndex(owner, "("); i >= 0 && strings.HasSuffix(owner, ")") {
		nick := strings.TrimSpace(owner[:i])
		login := strings.TrimSpace(owner[i+1 : len(owner)-1])
		if nick != "" && nick != owner {
			names = append(names, nick)
		}
		if login != "" && login != owner {
			names = append(names, login)
		}
	}
	return names
}

func matchUserByTAPDOwner(dir tapdUserDirectory, owner string) (domain.User, bool) {
	if dir == nil {
		return domain.User{}, false
	}
	for _, name := range tapdOwnerMatchNames(owner) {
		if user, ok := dir.UserByLoginName(name); ok {
			return user, true
		}
		for _, user := range dir.Users() {
			if strings.EqualFold(strings.TrimSpace(user.ChineseName), name) {
				return user, true
			}
		}
	}
	return domain.User{}, false
}

func (s *Service) resolveTAPDAssignee(store IssueStoreAdapter, projectID, owner string, fallbackUserID int64) int64 {
	if user, ok := matchUserByTAPDOwner(s.tapdUserDirectory(store), owner); ok {
		return user.ID
	}
	if fallbackUserID > 0 {
		return fallbackUserID
	}
	if project, ok := store.Project(projectID); ok {
		return project.OwnerID
	}
	return 0
}

func (s *Service) notifyRequirementImported(issue domain.Issue) {
	if s == nil || s.OnRequirementImported == nil {
		return
	}
	if !domain.ShouldAutoStartClarification(issue) {
		return
	}
	s.OnRequirementImported(issue)
}

func scoreStoredRequirement(store IssueStoreAdapter, issue *domain.Issue) {
	if issue == nil || !strings.EqualFold(issue.Kind, "requirement") {
		return
	}
	policy := domain.DefaultClarificationPolicy()
	if p, ok := store.Project(issue.ProjectID); ok {
		policy = domain.EffectiveClarificationPolicy(p.Clarification)
	}
	issue.Clarification = domain.ScoreRequirementIssue(*issue, policy)
}

func tapdWorkspaceFromURL(raw string) string {
	ref, err := ParseTAPDStoryURL(raw)
	if err != nil {
		return ""
	}
	return ref.WorkspaceID
}

// CreateTAPDStory pushes a local issue to TAPD, creating a new story.
func (s *Service) CreateTAPDStory(store IssueStoreAdapter, issue domain.Issue) (domain.Issue, error) {
	started := time.Now().UTC()
	workspaceID := ""
	iterationID := ""
	if issue.ProjectID != "" {
		if p, ok := store.Project(issue.ProjectID); ok {
			workspaceID = strings.TrimSpace(p.TAPDWorkspaceID)
			iterationID = strings.TrimSpace(p.TAPDIterationID)
		}
	}
	if workspaceID == "" || iterationID == "" {
		return issue, fmt.Errorf("project %q needs tapdWorkspaceId and tapdIterationId configured", issue.ProjectID)
	}
	client, _, err := s.trackerClientForProject("tapd", issue.ProjectID)
	if err != nil {
		return issue, err
	}
	args := map[string]any{
		"tool_name": "stories_create",
		"tool_args": map[string]any{
			"workspace_id": workspaceID,
			"name":         issue.Title,
			"description":  issue.Description,
			"iteration_id": iterationID,
		},
	}
	raw, err := client.CallTool("proxy_execute_tool", args)
	if err != nil {
		s.addLog(domain.SyncLog{
			ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "push", Source: "tapd",
			Action: "create_story", Status: "failed", Message: actionableAuthError("TAPD", err),
			ProjectID: issue.ProjectID,
			StartedAt: started, FinishedAt: time.Now().UTC(),
		})
		return issue, err
	}

	var st tapdStory
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		var arr []tapdStory
		if err2 := json.Unmarshal([]byte(raw), &arr); err2 == nil && len(arr) > 0 {
			st = arr[0]
		} else if extracted := extractTAPDStory(raw); extracted.ID != "" {
			st = extracted
		} else {
			return issue, fmt.Errorf("decode created story: %w", err)
		}
	}

	existed := false
	if id := strings.TrimSpace(issue.ID); id != "" {
		_, existed = store.Issue(id)
	}
	if issue.Kind == "" {
		issue.Kind = "requirement"
	}
	issue.Source = "tapd"
	issue.ExternalRef = &domain.ExternalRef{
		Source:        "tapd",
		ExternalID:    st.ID,
		ExternalURL:   fmt.Sprintf("https://tapd.example.com/tapd_fe/%s/story/detail/%s", workspaceID, st.ID),
		ExternalState: st.Status,
		ExternalType:  "story",
		SyncedAt:      time.Now().UTC(),
		Imported:      true,
	}
	if st.ID != "" {
		stableID := fmt.Sprintf("tapd-%s", st.ID)
		oldID := issue.ID
		issue.ID = stableID
		if oldID != "" && oldID != stableID && !strings.HasPrefix(oldID, "tapd-") {
			_ = store.DeleteIssue(oldID)
		}
	}
	// Intake scoring belongs to brand-new TAPD stories (import / 新建并同步).
	// Pushing an existing platform ticket must keep its description and
	// clarification; 直接创建 explicitly skipped analysis.
	if !existed {
		scoreStoredRequirement(store, &issue)
	}
	_ = store.PutIssue(issue)
	s.notifyRequirementImported(issue)

	s.addLog(domain.SyncLog{
		ID: fmt.Sprintf("sync-%d", started.UnixNano()), Direction: "push", Source: "tapd",
		Action: "create_story", Status: "success", Count: 1,
		Message:   fmt.Sprintf("创建 TAPD 需求 %s: %s", st.ID, st.Name),
		ProjectID: issue.ProjectID,
		StartedAt: started, FinishedAt: time.Now().UTC(),
	})
	return issue, nil
}

// --- Helpers ---

func parseTAPDStories(raw string) ([]tapdStory, error) {
	records, err := parseTAPDRecords(raw, "stories")
	if err != nil {
		return nil, err
	}
	return records, nil
}

// parseTAPDBugs parses bugs_get responses. Bugs reuse the same wire shape as
// stories except the title field is "title" instead of "name", which
// collectTAPDStories already normalizes into tapdStory.Name.
func parseTAPDBugs(raw string) ([]tapdStory, error) {
	return parseTAPDRecords(raw, "bugs")
}

func parseTAPDRecords(raw, kind string) ([]tapdStory, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || isTAPDEmptyResult(trimmed) {
		return []tapdStory{}, nil
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil, fmt.Errorf("%s", compactTAPDMessage(trimmed))
	}
	records := uniqueTAPDStories(collectTAPDStories(value))
	if records == nil {
		return nil, fmt.Errorf("could not parse TAPD %s response", kind)
	}
	return records, nil
}

func isTAPDEmptyResult(raw string) bool {
	return strings.Contains(raw, "未找到匹配的需求记录") || strings.Contains(raw, "未找到匹配") || strings.Contains(raw, "未查询到符合条件的")
}

// isTAPDEmptyError reports whether err is TAPD answering "no records matched"
// through the MCP is_error channel (e.g. bugs_get on an iteration without
// bugs), which callers should treat as an empty page rather than a failure.
func isTAPDEmptyError(err error) bool {
	if err == nil {
		return false
	}
	return isTAPDEmptyResult(err.Error())
}

func compactTAPDMessage(raw string) string {
	line := strings.SplitN(raw, "\n", 2)[0]
	line = strings.TrimSpace(line)
	if line == "" {
		return "TAPD 返回了无法解析的内容"
	}
	runes := []rune(line)
	if len(runes) > 120 {
		line = string(runes[:120]) + "…"
	}
	return "TAPD：" + line
}

func tapdPullMessage(iterationID string, count, removed int) string {
	switch {
	case count == 0 && removed == 0:
		if iterationID == "" {
			return "该迭代下暂无 TAPD 需求/缺陷"
		}
		return fmt.Sprintf("迭代 %s 下暂无 TAPD 需求/缺陷", iterationID)
	case count == 0 && removed > 0:
		return fmt.Sprintf("迭代 %s 下暂无 TAPD 需求/缺陷，已移除本地 %d 条", iterationID, removed)
	case removed > 0:
		return fmt.Sprintf("同步 %d 条 TAPD 需求/缺陷，移除 %d 条已不在该迭代中的项", count, removed)
	default:
		return fmt.Sprintf("同步 %d 条 TAPD 需求/缺陷", count)
	}
}

func tapdExternalID(issue domain.Issue) string {
	if issue.ExternalRef != nil && strings.TrimSpace(issue.ExternalRef.ExternalID) != "" {
		return strings.TrimSpace(issue.ExternalRef.ExternalID)
	}
	id := strings.TrimPrefix(issue.ID, "tapd-")
	if i := strings.LastIndexByte(id, '-'); i >= 0 {
		id = id[i+1:]
	}
	return id
}

func retainImportedTAPDIssue(issue domain.Issue) bool {
	if issue.ExternalRef != nil && issue.ExternalRef.Imported {
		return true
	}
	// Older imports only flipped clarification.gated; keep those too.
	return issue.Clarification.Gated
}

func reconcileTAPDIssues(store IssueStoreAdapter, remoteIDs map[string]bool) int {
	removed := 0
	for _, issue := range store.Issues() {
		if issue.Source != "tapd" {
			continue
		}
		if retainImportedTAPDIssue(issue) {
			continue
		}
		if remoteIDs[tapdExternalID(issue)] {
			continue
		}
		if err := store.DeleteIssue(issue.ID); err != nil {
			log.Printf("remove stale tapd issue %s: %v", issue.ID, err)
			continue
		}
		removed++
	}
	return removed
}

func uniqueTAPDStories(stories []tapdStory) []tapdStory {
	seen := map[string]bool{}
	out := make([]tapdStory, 0, len(stories))
	for _, story := range stories {
		story.ID = strings.TrimSpace(story.ID)
		if story.ID == "" || seen[story.ID] {
			continue
		}
		seen[story.ID] = true
		out = append(out, story)
	}
	return out
}

func collectTAPDStories(value any) []tapdStory {
	var out []tapdStory
	var walk func(any)
	walk = func(current any) {
		switch item := current.(type) {
		case []any:
			for _, child := range item {
				walk(child)
			}
		case map[string]any:
			for _, key := range []string{"Story", "story", "Bug", "bug", "Task", "task"} {
				if wrapped, ok := item[key]; ok {
					walk(wrapped)
					return
				}
			}
			if id, ok := item["id"]; ok && fmt.Sprint(id) != "" && (item["name"] != nil || item["title"] != nil) {
				encoded, _ := json.Marshal(item)
				var story tapdStory
				if json.Unmarshal(encoded, &story) == nil {
					if story.Name == "" {
						story.Name = fmt.Sprint(item["title"])
					}
					out = append(out, story)
				}
				return
			}
			for _, key := range []string{"data", "items", "stories", "bugs", "tasks", "results"} {
				if child, ok := item[key]; ok {
					walk(child)
				}
			}
		}
	}
	walk(value)
	return out
}

func extractTAPDStory(raw string) tapdStory {
	var nested struct {
		Data struct {
			Story tapdStory `json:"Story"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(raw), &nested) == nil {
		return nested.Data.Story
	}
	var direct struct {
		Story tapdStory `json:"Story"`
	}
	if json.Unmarshal([]byte(raw), &direct) == nil {
		return direct.Story
	}
	return tapdStory{}
}

func gongfengStateToStatus(state string) string {
	switch state {
	case "opened":
		return "todo"
	case "closed":
		return "closed"
	default:
		return "backlog"
	}
}

func gongfengPriority(p *int) string {
	if p == nil {
		return "medium"
	}
	switch *p {
	case 1, 2:
		return "urgent"
	case 3:
		return "high"
	case 4:
		return "medium"
	default:
		return "low"
	}
}

func tapdStatusToStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open", "开发中", "开发-中":
		return "in_progress"
	case "resolved", "已解决":
		return "in_review"
	case "closed", "已关闭":
		return "closed"
	case "done", "status_4", "合入", "已合入", "已完成", "完成":
		return "done"
	case "rejected", "已拒绝":
		return "cancelled"
	case "planned", "规划中":
		return "todo"
	default:
		return "backlog"
	}
}

// TAPD task statuses have different meanings from story statuses: an open
// task has not started, and progressing is the active development state.
func tapdTaskStatusToStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "open":
		return "todo"
	case "progressing":
		return "in_progress"
	default:
		return tapdStatusToStatus(status)
	}
}

func tapdTaskPriority(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "high":
		return "high"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

// tapdBugStatusToStatus maps TAPD bug workflow statuses (new / in_progress /
// resolved / verified / closed / reopened / rejected ...) onto issue statuses.
func tapdBugStatusToStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "new", "新建":
		return "todo"
	case "in_progress", "处理中", "修复中", "开发中":
		return "in_progress"
	case "reopened", "重新打开":
		return "in_progress"
	case "resolved", "已解决":
		return "in_review"
	case "verified", "已验证":
		return "in_review"
	case "closed", "已关闭":
		return "closed"
	case "rejected", "已拒绝", "invalid", "无效":
		return "cancelled"
	case "postponed", "挂起", "推迟":
		return "backlog"
	default:
		return "backlog"
	}
}

func closeSourceForStatus(status string) string {
	if status == "closed" {
		return "external"
	}
	return ""
}

func tapdPriority(p string) string {
	switch strings.ToLower(p) {
	case "high", "紧急":
		return "urgent"
	case "middle", "中":
		return "medium"
	case "low", "低":
		return "low"
	default:
		return "medium"
	}
}

func (s *Service) authError(source, origin string, err error) string {
	message := actionableAuthError(source, err)
	if origin != "" && strings.Contains(message, "401") {
		return message + "（当前鉴权：" + origin + "）"
	}
	return message
}

func parseTime(s string) time.Time {
	formats := []string{
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05+0000",
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func actionableAuthError(source string, err error) string {
	message := err.Error()
	if strings.Contains(message, "401") || strings.Contains(strings.ToLower(message), "unauthorized") {
		return source + "认证已失效（HTTP 401）。同步应使用资产目录里对应 HTTP MCP 的 Headers，请检查工蜂/TAPD MCP 资产后重试"
	}
	return message
}

func findGongfengIssue(store IssueStoreAdapter, repo domain.Repository, gi gongfengIssue) (domain.Issue, bool) {
	var matches []domain.Issue
	seen := map[string]bool{}
	add := func(issue domain.Issue) {
		if issue.ID == "" || seen[issue.ID] || !sameGongfengRepo(issue, repo, gi.IID) {
			return
		}
		seen[issue.ID] = true
		matches = append(matches, issue)
	}
	if repo.ID != "" {
		if issue, ok := store.Issue(fmt.Sprintf("gf-%s-%d", repo.ID, gi.IID)); ok {
			add(issue)
		}
	}
	if issue, ok := store.Issue(fmt.Sprintf("gf-%d", gi.IID)); ok {
		add(issue)
	}
	for _, issue := range store.Issues() {
		if issue.Source != "gongfeng" {
			continue
		}
		iid := 0
		if issue.ExternalRef != nil {
			iid = issue.ExternalRef.ExternalIID
		}
		if iid == 0 {
			iid = parseGongfengIID(issue.ID)
		}
		if iid != gi.IID {
			continue
		}
		add(issue)
	}
	if len(matches) == 0 {
		return domain.Issue{}, false
	}
	return pickIssueKeeper(matches), true
}

func sameGongfengRepo(issue domain.Issue, repo domain.Repository, iid int) bool {
	if strings.TrimSpace(repo.ID) == "" {
		return false
	}
	if repoID := strings.TrimSpace(issue.RepositoryID); repoID != "" {
		return repoID == repo.ID
	}
	if issue.ExternalRef != nil {
		if path := gongfengProjectPathFromURL(issue.ExternalRef.ExternalURL); path != "" {
			return strings.EqualFold(path, strings.TrimSpace(repo.GongfengProjectID)) || strings.EqualFold(path, gongfengProjectPathFromURL(strings.TrimSuffix(repo.WebURL, "/")+"/issues/0"))
		}
	}
	if parsedRepo, parsedIID := parseGongfengIssueID(issue.ID); parsedIID == iid && parsedRepo != "" {
		return parsedRepo == repo.ID
	}
	return false
}

func tapdExternalType(issue domain.Issue) string {
	if issue.ExternalRef != nil {
		kind := strings.TrimSpace(issue.ExternalRef.ExternalType)
		if kind == "story" || kind == "bug" || kind == "task" {
			return kind
		}
		if ref, err := ParseTAPDStoryURL(issue.ExternalRef.ExternalURL); err == nil {
			return ref.Kind
		}
	}
	if strings.HasPrefix(issue.ID, "tapd-task-") {
		return "task"
	}
	if issue.Kind == "bug" {
		return "bug"
	}
	return "story"
}

func tapdIssueIsTask(issue domain.Issue) bool {
	return tapdExternalType(issue) == "task"
}

func findTAPDIssue(store IssueStoreAdapter, storyID string) (domain.Issue, bool) {
	return findTAPDRecord(store, storyID, "", "")
}

func findTAPDRecord(store IssueStoreAdapter, recordID, kind, workspaceID string) (domain.Issue, bool) {
	stableID := "tapd-" + recordID
	if kind == "task" {
		stableID = "tapd-task-" + recordID
	}
	matches := func(issue domain.Issue) bool {
		if issue.Source != "tapd" || tapdExternalID(issue) != recordID {
			return false
		}
		if kind != "" && tapdExternalType(issue) != kind {
			return false
		}
		if kind == "" && tapdIssueIsTask(issue) {
			return false
		}
		storedWorkspace := ""
		if issue.ExternalRef != nil {
			storedWorkspace = tapdWorkspaceFromURL(issue.ExternalRef.ExternalURL)
		}
		if storedWorkspace == "" {
			if project, ok := store.Project(issue.ProjectID); ok {
				storedWorkspace = strings.TrimSpace(project.TAPDWorkspaceID)
			}
		}
		return workspaceID == "" || storedWorkspace == "" || storedWorkspace == workspaceID
	}
	if issue, ok := store.Issue(stableID); ok && matches(issue) {
		return issue, true
	}
	for _, issue := range store.Issues() {
		if matches(issue) {
			return issue, true
		}
	}
	return domain.Issue{}, false
}

func applyExistingIssue(dst *domain.Issue, existing domain.Issue) {
	previousExternalClosed := existing.TrackerClosed()
	remoteClosed := dst.Status == "closed"
	dst.ID = existing.ID
	dst.CreatedAt = existing.CreatedAt
	if existing.Number > 0 {
		dst.Number = existing.Number
	}
	dst.AssigneeType = existing.AssigneeType
	dst.AssigneeID = existing.AssigneeID
	dst.AssigneeUserID = existing.AssigneeUserID
	dst.WorkflowID = existing.WorkflowID
	dst.LatestTaskID = existing.LatestTaskID
	dst.AutomationState = existing.AutomationState
	if existing.ManuallyClosed() {
		dst.CloseSource = "manual"
	} else if previousExternalClosed && !remoteClosed && !existing.DeliveryLocked() {
		// The external work item reopened; scheduled execution may resume.
		dst.AutomationState = "eligible"
	}
	dst.DeliveryState = existing.DeliveryState
	dst.DeliveryMRURL = existing.DeliveryMRURL
	dst.DeliveryMRState = existing.DeliveryMRState
	dst.DeliveryMRs = existing.DeliveryMRs
	dst.DeliveryUpdatedAt = existing.DeliveryUpdatedAt
	if existing.Kind != "" {
		dst.Kind = existing.Kind
	}
	dst.Clarification = existing.Clarification
	if existing.ExternalRef != nil && existing.ExternalRef.Imported {
		if dst.ExternalRef == nil {
			ref := *existing.ExternalRef
			dst.ExternalRef = &ref
		} else {
			dst.ExternalRef.Imported = true
		}
	}
	if existing.ProjectID != "" {
		dst.ProjectID = existing.ProjectID
	}
	if existing.RepositoryID != "" {
		dst.RepositoryID = existing.RepositoryID
	}
	if len(existing.RepositoryIDs) > 0 {
		// Remote trackers are single-repository; keep the local multi-repo
		// binding intact so scheduled SOPs keep touching every repository.
		dst.RepositoryIDs = append([]string(nil), existing.RepositoryIDs...)
		if dst.RepositoryID == "" {
			dst.RepositoryID = existing.RepositoryIDs[0]
		}
	}
	if existing.BaseBranch != "" {
		dst.BaseBranch = existing.BaseBranch
	}
	if keepLocalExecutionStatus(existing, dst.Status) {
		dst.Status = existing.Status
	} else if existing.CloseSource == "external" && !remoteClosed && existing.DeliveryLocked() {
		// A completed local delivery remains done after the tracker reopens.
		dst.Status = "done"
	}
	if dst.Status != "closed" {
		dst.CloseSource = ""
	}
}

func keepLocalExecutionStatus(existing domain.Issue, remote string) bool {
	if existing.ManuallyClosed() {
		return true
	}
	if remote == "closed" || existing.CloseSource == "external" {
		return false
	}
	if existing.DeliveryLocked() {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(existing.DeliveryState), "reopened") {
		return true
	}
	switch existing.Status {
	case "in_progress", "blocked", "in_review":
		return remote != "done" && remote != "cancelled"
	default:
		return false
	}
}

func pruneDuplicateExternalIssues(store IssueStoreAdapter) {
	groups := map[string][]domain.Issue{}
	for _, issue := range store.Issues() {
		key := externalIssueKey(issue)
		if key == "" {
			continue
		}
		groups[key] = append(groups[key], issue)
	}
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		keeper := pickIssueKeeper(group)
		for _, extra := range group {
			if extra.ID == keeper.ID {
				continue
			}
			keeper = mergeIssueKeep(keeper, extra)
			_ = store.DeleteIssue(extra.ID)
		}
		_ = store.PutIssue(keeper)
	}
}

func externalIssueKey(issue domain.Issue) string {
	if issue.Source == "gongfeng" {
		iid := 0
		if issue.ExternalRef != nil {
			iid = issue.ExternalRef.ExternalIID
		}
		if iid == 0 {
			iid = parseGongfengIID(issue.ID)
		}
		if iid == 0 {
			return ""
		}
		scope := gongfengIssueScope(issue)
		if scope == "" {
			return ""
		}
		return fmt.Sprintf("gongfeng:%s:%d", scope, iid)
	}
	if issue.Source == "tapd" {
		id := tapdExternalID(issue)
		if id == "" {
			return ""
		}
		workspaceID := ""
		if issue.ExternalRef != nil {
			workspaceID = tapdWorkspaceFromURL(issue.ExternalRef.ExternalURL)
		}
		return fmt.Sprintf("tapd:%s:%s:%s", workspaceID, tapdExternalType(issue), id)
	}
	return ""
}

func pickIssueKeeper(issues []domain.Issue) domain.Issue {
	best := issues[0]
	for _, issue := range issues[1:] {
		if betterIssue(issue, best) {
			best = issue
		}
	}
	return best
}

func betterIssue(a, b domain.Issue) bool {
	if (a.LatestTaskID != "") != (b.LatestTaskID != "") {
		return a.LatestTaskID != ""
	}
	if (a.WorkflowID != "") != (b.WorkflowID != "") {
		return a.WorkflowID != ""
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	if len(a.ID) != len(b.ID) {
		return len(a.ID) < len(b.ID)
	}
	return a.ID < b.ID
}

func parseGongfengIID(id string) int {
	_, iid := parseGongfengIssueID(id)
	return iid
}

func parseGongfengIssueID(id string) (repo string, iid int) {
	body := strings.TrimPrefix(strings.TrimSpace(id), "gf-")
	if body == "" || body == strings.TrimSpace(id) {
		return "", 0
	}
	if n, err := strconv.Atoi(body); err == nil {
		return "", n
	}
	i := strings.LastIndex(body, "-")
	if i <= 0 {
		return "", 0
	}
	n, err := strconv.Atoi(body[i+1:])
	if err != nil || n <= 0 {
		return "", 0
	}
	return body[:i], n
}

func gongfengIssueScope(issue domain.Issue) string {
	if repo := strings.TrimSpace(issue.RepositoryID); repo != "" {
		return repo
	}
	if issue.ExternalRef != nil {
		if path := gongfengProjectPathFromURL(issue.ExternalRef.ExternalURL); path != "" {
			return path
		}
	}
	if repo, _ := parseGongfengIssueID(issue.ID); repo != "" {
		return repo
	}
	return ""
}

func gongfengProjectPathFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	path := raw
	if u, err := url.Parse(raw); err == nil && strings.TrimSpace(u.Path) != "" {
		path = u.Path
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	for i, part := range parts {
		if part != "issues" {
			continue
		}
		prefix := parts[:i]
		if n := len(prefix); n > 0 && prefix[n-1] == "-" {
			prefix = prefix[:n-1]
		}
		if len(prefix) >= 2 {
			return strings.Join(prefix, "/")
		}
	}
	return ""
}

func mergeIssueKeep(keeper, extra domain.Issue) domain.Issue {
	if keeper.LatestTaskID == "" {
		keeper.LatestTaskID = extra.LatestTaskID
	}
	if keeper.WorkflowID == "" {
		keeper.WorkflowID = extra.WorkflowID
	}
	if keeper.AssigneeID == "" {
		keeper.AssigneeID = extra.AssigneeID
		keeper.AssigneeType = extra.AssigneeType
	}
	if keeper.RepositoryID == "" {
		keeper.RepositoryID = extra.RepositoryID
	}
	if keeper.Clarification.Score == 0 && extra.Clarification.Score > 0 {
		keeper.Clarification = extra.Clarification
	}
	if deliveryFieldsPresent(extra) {
		if !deliveryFieldsPresent(keeper) || extra.DeliveryUpdatedAt.After(keeper.DeliveryUpdatedAt) {
			keeper = copyDeliveryFields(keeper, extra)
		}
	}
	return keeper
}

func deliveryFieldsPresent(issue domain.Issue) bool {
	return strings.TrimSpace(issue.DeliveryState) != ""
}

func copyDeliveryFields(dst, src domain.Issue) domain.Issue {
	dst.DeliveryState = src.DeliveryState
	dst.DeliveryMRURL = src.DeliveryMRURL
	dst.DeliveryMRState = src.DeliveryMRState
	dst.DeliveryMRs = src.DeliveryMRs
	dst.DeliveryUpdatedAt = src.DeliveryUpdatedAt
	return dst
}

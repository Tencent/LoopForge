package httpapi

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

const (
	issueListMaxLimit     = 200
	issueListDefaultLimit = 50
)

type issueListPage struct {
	Items   []issueDeletionView `json:"items"`
	Total   int                 `json:"total"`
	Offset  int                 `json:"offset"`
	Limit   int                 `json:"limit"`
	HasMore bool                `json:"hasMore"`
}

// issuesHandlers 依赖 tasks（列表/详情触发任务状态同步）与 stream（发起 clarify）。
type issuesHandlers struct {
	*deps
	tasks  *tasksHandlers
	stream *streamHandlers
}

func (h issuesHandlers) importTAPD(c *gin.Context) {
	var req struct {
		ProjectID     string   `json:"projectId"`
		URL           string   `json:"url"`
		WorkflowID    string   `json:"workflowId"`
		RepositoryID  string   `json:"repositoryId"`
		RepositoryIDs []string `json:"repositoryIds"`
		BaseBranch    *string  `json:"baseBranch"`
	}
	if !decode(c, &req) {
		return
	}
	if strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.URL) == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "projectId and url are required"})
		return
	}
	if _, err := tracker.ParseTAPDStoryURL(req.URL); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if _, ok := h.repo.Project(req.ProjectID); !ok {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "project not found"})
		return
	}
	ids := make([]string, 0, len(req.RepositoryIDs)+1)
	seen := map[string]bool{}
	for _, id := range req.RepositoryIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if id := strings.TrimSpace(req.RepositoryID); id != "" && !seen[id] {
		ids = append([]string{id}, ids...)
	}
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "请至少选择一个代码仓库"})
		return
	}
	probe := domain.Issue{ProjectID: req.ProjectID, RepositoryIDs: ids, RepositoryID: ids[0]}
	if req.BaseBranch != nil {
		probe.BaseBranch = *req.BaseBranch
	}
	if err := h.bindIssueRepositories(&probe); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if h.sync == nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "TAPD 同步未配置"})
		return
	}
	h.configureSync(h.repo.SyncConfig())
	var fallbackUserID int64
	if current, ok := h.requestUser(c.Request, true); ok {
		fallbackUserID = current.ID
	}
	issue, err := h.sync.ImportTAPDStory(h.repo, req.ProjectID, req.URL, fallbackUserID)
	if err != nil {
		respond(c, err)
		return
	}
	previous := issue
	if err := h.assignIssueWorkflow(&issue, req.WorkflowID); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	h.bindRequirementPlaybook(&issue)
	if req.BaseBranch != nil || issue.RepositoryID != probe.RepositoryID {
		issue.BaseBranch = probe.BaseBranch
	}
	issue.RepositoryIDs = probe.RepositoryIDs
	issue.RepositoryID = probe.RepositoryID
	if req.BaseBranch == nil {
		h.normalizeLegacyIssueBaseBranch(&issue)
	}
	if err := h.bindIssueRepositories(&issue); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	h.invalidateIssueClarifySessionOnRepositoryChange(&issue, previous)
	if err := h.repo.PutIssue(issue); err != nil {
		respond(c, err)
		return
	}
	stored, _ := h.repo.Issue(issue.ID)
	if stored.ID != "" {
		go h.stream.autoClarifyIssue(stored.ID)
	}
	c.JSON(http.StatusOK, stored)
}

func (h issuesHandlers) issues(c *gin.Context) {
	r := c.Request
	if r.Method == http.MethodGet {
		h.tasks.syncTasks()
		items := filterIssues(h.repo.Issues(), issueListQuery{
			ProjectID: strings.TrimSpace(c.Query("projectId")),
			Status:    strings.TrimSpace(c.Query("status")),
			Source:    strings.TrimSpace(c.Query("source")),
			Q:         strings.TrimSpace(c.Query("q")),
			Assignee:  strings.TrimSpace(c.Query("assignee")),
		})
		if _, ok := c.GetQuery("limit"); !ok {
			views, err := h.issueDeletionViews(items)
			if err != nil {
				respond(c, err)
				return
			}
			c.JSON(200, views)
			return
		}
		limit, err := parseIssueListInt(c.Query("limit"), issueListDefaultLimit)
		if err != nil {
			c.JSON(400, gin.H{"error": "limit 必须是整数"})
			return
		}
		if limit < 1 {
			limit = issueListDefaultLimit
		}
		if limit > issueListMaxLimit {
			limit = issueListMaxLimit
		}
		offset := 0
		if raw, exists := c.GetQuery("offset"); exists && strings.TrimSpace(raw) != "" {
			offset, err = parseIssueListInt(raw, 0)
			if err != nil || offset < 0 {
				c.JSON(400, gin.H{"error": "offset 必须是非负整数"})
				return
			}
		}
		total := len(items)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		page := items[offset:end]
		if page == nil {
			page = []domain.Issue{}
		}
		views, err := h.issueDeletionViews(page)
		if err != nil {
			respond(c, err)
			return
		}
		c.JSON(200, issueListPage{Items: views, Total: total, Offset: offset, Limit: limit, HasMore: end < total})
		return
	}
	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		var req struct {
			domain.Issue
			SyncTarget string  `json:"syncTarget"`
			TapdID     *string `json:"tapdId"`
			BaseBranch *string `json:"baseBranch"`
		}
		if !decode(c, &req) {
			return
		}
		v := req.Issue
		existing, found := h.repo.Issue(v.ID)
		if !v.Clarification.StartedAt.IsZero() && v.Clarification.StartedAt.After(time.Now().UTC().Add(time.Minute)) {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "澄清开始时间不能晚于当前时间"})
			return
		}
		if req.TapdID != nil {
			v.TapdID = strings.TrimSpace(*req.TapdID)
		} else if found {
			v.TapdID = existing.TapdID
		}
		if found {
			if !h.requireProjectWriter(c, v.ProjectID, existing.ProjectID) {
				return
			}
			v = mergeIssueEdit(v, existing)
		} else if !h.requireProjectWriter(c, v.ProjectID) {
			return
		}
		// An explicit empty branch restores automatic branch creation; an omitted
		// field preserves the selection on edits made by older clients.
		if req.BaseBranch != nil {
			v.BaseBranch = *req.BaseBranch
		}
		if found && req.BaseBranch == nil {
			h.normalizeLegacyIssueBaseBranch(&v)
		}
		if !found && v.AssigneeUserID == 0 {
			if current, ok := h.requestUser(c.Request, true); ok && current != nil && current.ID > 0 {
				v.AssigneeUserID = current.ID
			}
		}
		if v.ProjectID == "" || v.Title == "" {
			c.JSON(400, map[string]string{"error": "projectId and title are required"})
			return
		}
		if v.Status == "" {
			v.Status = "backlog"
		}
		if v.Priority == "" {
			v.Priority = "medium"
		}
		if _, ok := h.repo.Project(v.ProjectID); !ok {
			c.JSON(400, map[string]string{"error": "project not found"})
			return
		}
		h.bindRequirementPlaybook(&v)
		if v.AssigneeUserID > 0 {
			if _, ok := h.repo.User(v.AssigneeUserID); !ok {
				c.JSON(400, map[string]string{"error": "assignee must reference an existing user"})
				return
			}
		}
		if err := h.bindIssueRepositories(&v); err != nil {
			c.JSON(400, map[string]string{"error": err.Error()})
			return
		}
		if found {
			h.invalidateIssueClarifySessionOnRepositoryChange(&v, existing)
		}
		if strings.EqualFold(req.SyncTarget, "tapd") && v.Kind == "" {
			v.Kind = "requirement"
		}
		if err := validateManualTapdID(v, req.SyncTarget); err != nil {
			respond(c, err)
			return
		}
		// Import scoring and auto-clarify are owned by TAPD import / create,
		// not by issues_save. Binding SOP or repositories on a synced ticket
		// must not gate it as "待澄清".
		if found {
			keepExistingTrackerBinding(&v, existing)
		} else {
			keepExistingTrackerBinding(&v, domain.Issue{})
		}
		if !found && !repo.IssueBoundToTracker(v) {
			target := strings.ToLower(strings.TrimSpace(req.SyncTarget))
			switch target {
			case "gongfeng", "tapd":
				created, err := h.createIssueOnTracker(v, target)
				if err != nil {
					respond(c, err)
					return
				}
				stored, _ := h.repo.Issue(created.ID)
				if stored.ID != "" {
					h.recordIssueOperation(c, &stored, "create", "创建需求")
					if err := h.repo.PutIssue(stored); err != nil {
						respond(c, err)
						return
					}
					stored, _ = h.repo.Issue(stored.ID)
					go h.stream.autoClarifyIssue(stored.ID)
				}
				h.writeIssueView(c, 200, stored)
				return
			case "", "local":
				if v.Kind == "" {
					v.Kind = "requirement"
				}
				v.Source = "local"
				v.ExternalRef = nil
			default:
				c.JSON(400, map[string]string{"error": "syncTarget 只能是 local、gongfeng 或 tapd"})
				return
			}
		}
		if v.ID == "" {
			v.ID = fmt.Sprintf("issue-%d", time.Now().UnixNano())
		}
		if !found {
			h.recordIssueOperation(c, &v, "create", "创建需求")
		} else if detail := issueChangeDetail(existing, v); detail != "" {
			h.recordIssueOperation(c, &v, "update", detail)
		}
		if err := h.repo.PutIssue(v); err != nil {
			respond(c, err)
			return
		}
		stored, _ := h.repo.Issue(v.ID)
		h.writeIssueView(c, 200, stored)
		return
	}
}

// mergeIssueEdit fills fields the editor left empty from the stored issue.
// The editor payload does not round-trip the audit log, so the server copy
// of Operations always wins.
func mergeIssueEdit(v, existing domain.Issue) domain.Issue {
	if v.ProjectID == "" {
		v.ProjectID = existing.ProjectID
	}
	if v.Title == "" {
		v.Title = existing.Title
	}
	if v.LatestTaskID == "" {
		v.LatestTaskID = existing.LatestTaskID
	}
	if v.Source == "" {
		v.Source = existing.Source
	}
	if v.ExternalRef == nil {
		v.ExternalRef = existing.ExternalRef
	}
	if len(v.RepositoryIDs) == 0 && v.RepositoryID == "" {
		v.RepositoryIDs = existing.RepositoryIDs
		v.RepositoryID = existing.RepositoryID
	}
	if v.BaseBranch == "" {
		v.BaseBranch = existing.BaseBranch
	}
	if v.Kind == "" {
		v.Kind = existing.Kind
	}
	if v.AssigneeID == "" {
		v.AssigneeID = existing.AssigneeID
	}
	if v.AssigneeType == "" {
		v.AssigneeType = existing.AssigneeType
	}
	if v.AssigneeUserID == 0 {
		v.AssigneeUserID = existing.AssigneeUserID
	}
	if v.WorkflowID == "" {
		v.WorkflowID = existing.WorkflowID
	}
	if v.SOPBindings == nil && existing.SOPBindings != nil {
		v.SOPBindings = existing.SOPBindings
	}
	if v.PlaybookID == "" {
		v.PlaybookID = existing.PlaybookID
	}
	if v.Clarification.Score == 0 && !v.Clarification.Passed && len(v.Clarification.Answers) == 0 {
		v.Clarification = existing.Clarification
	}
	v.Operations = append([]domain.IssueOperation(nil), existing.Operations...)
	return v
}

// validateManualTapdID checks a hand-entered TAPD ID, which only applies to
// platform-native issues.
func validateManualTapdID(v domain.Issue, syncTarget string) error {
	if v.TapdID == "" {
		return nil
	}
	if v.ExternalRef != nil || strings.EqualFold(v.Source, "tapd") || strings.EqualFold(v.Source, "gongfeng") || strings.EqualFold(syncTarget, "tapd") || strings.EqualFold(syncTarget, "gongfeng") {
		return HTTPError{Status: http.StatusBadRequest, Message: "手动 TAPD ID 仅用于平台需求；同步单请使用原始外部 ID"}
	}
	for _, digit := range v.TapdID {
		if digit < '0' || digit > '9' {
			return HTTPError{Status: http.StatusBadRequest, Message: "TAPD ID 只能包含数字"}
		}
	}
	return nil
}

// issueRoute is one /api/issues/:id sub-resource. Routes are registered with
// Any, so unsupported methods fall back to http.NotFound (404 text/plain).
type issueRoute struct {
	handle  func(issuesHandlers, *gin.Context, domain.Issue)
	methods []string
}

var issueRoutes = map[string]issueRoute{
	"/api/issues/:id":                     {handle: issuesHandlers.issueDetail, methods: []string{http.MethodGet, http.MethodDelete}},
	"/api/issues/:id/comments":            {handle: issuesHandlers.addIssueComment, methods: []string{http.MethodPost}},
	"/api/issues/:id/runs":                {handle: issuesHandlers.issueRuns, methods: []string{http.MethodGet}},
	"/api/issues/:id/clarify":             {handle: issuesHandlers.assessIssueClarification, methods: []string{http.MethodPost}},
	"/api/issues/:id/clarification":       {handle: issuesHandlers.submitIssueClarification, methods: []string{http.MethodPost}},
	"/api/issues/:id/chat-clarify":        {handle: issuesHandlers.chatClarifyIssue, methods: []string{http.MethodGet, http.MethodPost}},
	"/api/issues/:id/chat-clarify/cancel": {handle: issuesHandlers.cancelChatClarifyIssue, methods: []string{http.MethodPost}},
	"/api/issues/:id/reopen":              {handle: issuesHandlers.reopenIssue, methods: []string{http.MethodPost}},
	"/api/issues/:id/close":               {handle: issuesHandlers.closeIssue, methods: []string{http.MethodPost}},
	"/api/issues/:id/resync-tapd":         {handle: issuesHandlers.resyncTAPDIssue, methods: []string{http.MethodPost}},
	"/api/issues/:id/requirement":         {handle: issuesHandlers.issueRequirement, methods: []string{http.MethodGet}},
	"/api/issues/:id/sops":                {handle: issuesHandlers.issueSOPs, methods: []string{http.MethodGet, http.MethodPut}},
	"/api/issues/:id/dispatch":            {handle: issuesHandlers.dispatchIssueRoute, methods: []string{http.MethodPost}},
}

func (h issuesHandlers) issue(c *gin.Context) {
	issue, ok := h.repo.Issue(c.Param("id"))
	if !ok {
		c.JSON(404, map[string]string{"error": "issue not found"})
		return
	}
	route, ok := issueRoutes[c.FullPath()]
	if !ok || !slices.Contains(route.methods, c.Request.Method) {
		http.NotFound(c.Writer, c.Request)
		return
	}
	if c.Request.Method != http.MethodGet && !h.requireProjectWriter(c, issue.ProjectID) {
		return
	}
	if c.FullPath() == "/api/issues/:id" && c.Request.Method == http.MethodDelete {
		h.deleteIssue(c, issue)
		return
	}
	route.handle(h, c, issue)
}

func actorLabel(user *auth.User) (string, int64) {
	if user == nil {
		return "", 0
	}
	name := strings.TrimSpace(user.LoginName)
	cn := strings.TrimSpace(user.ChineseName)
	switch {
	case name != "" && cn != "":
		return name + "(" + cn + ")", user.ID
	case cn != "":
		return cn, user.ID
	default:
		return name, user.ID
	}
}

func (h issuesHandlers) recordIssueOperation(c *gin.Context, issue *domain.Issue, action, detail string) {
	if issue == nil {
		return
	}
	user, _ := h.requestUser(c.Request, true)
	actor, id := actorLabel(user)
	issue.AppendOperation(domain.IssueOperation{Actor: actor, ActorUserID: id, Action: action, Detail: detail})
}

func (h issuesHandlers) appendStoredOperation(c *gin.Context, issueID, action, detail string) (domain.Issue, error) {
	stored, ok := h.repo.Issue(issueID)
	if !ok {
		return domain.Issue{}, fmt.Errorf("issue not found")
	}
	h.recordIssueOperation(c, &stored, action, detail)
	if err := h.repo.PutIssue(stored); err != nil {
		return domain.Issue{}, err
	}
	saved, _ := h.repo.Issue(issueID)
	return saved, nil
}

func issueStatusName(status string) string {
	switch status {
	case "backlog":
		return "待规划"
	case "todo":
		return "待处理"
	case "in_progress":
		return "进行中"
	case "in_review":
		return "评审中"
	case "done":
		return "已完成"
	case "closed":
		return "已关闭"
	case "blocked":
		return "已阻塞"
	case "cancelled":
		return "已取消"
	default:
		if strings.TrimSpace(status) == "" {
			return "未设置"
		}
		return status
	}
}

func issueChangeDetail(before, after domain.Issue) string {
	var parts []string
	if before.Status != after.Status && after.Status != "" {
		parts = append(parts, "状态 "+issueStatusName(before.Status)+" → "+issueStatusName(after.Status))
	}
	if strings.TrimSpace(before.Title) != strings.TrimSpace(after.Title) && strings.TrimSpace(after.Title) != "" {
		parts = append(parts, "标题")
	}
	if strings.TrimSpace(before.Description) != strings.TrimSpace(after.Description) {
		parts = append(parts, "描述")
	}
	if before.AssigneeUserID != after.AssigneeUserID || before.AssigneeID != after.AssigneeID {
		parts = append(parts, "责任人")
	}
	if before.WorkflowID != after.WorkflowID && after.WorkflowID != "" {
		parts = append(parts, "关联 SOP")
	}
	if before.TapdID != after.TapdID {
		if after.TapdID == "" {
			parts = append(parts, "清除统计关联 TAPD ID")
		} else {
			parts = append(parts, "统计关联 TAPD ID "+after.TapdID)
		}
	}
	if before.BaseBranch != after.BaseBranch {
		if after.BaseBranch == "" {
			parts = append(parts, "工作分支改为自动新建")
		} else {
			parts = append(parts, "继续已有工作分支 "+after.BaseBranch)
		}
	}
	if before.Priority != after.Priority && after.Priority != "" {
		parts = append(parts, "优先级")
	}
	return strings.Join(parts, "、")
}

// promoteClarifiedIssueTodo 澄清完成后，把尚未进入业务流转的需求归入「待处理」（todo）。
// 已在执行链路中的状态（todo/in_progress/in_review/blocked/done/closed 等）不做降级改写。
func promoteClarifiedIssueTodo(issue *domain.Issue) {
	switch strings.ToLower(strings.TrimSpace(issue.Status)) {
	case "", "backlog":
		issue.Status = "todo"
	}
}

func appendClarificationDescription(description, summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return description
	}
	const heading = "## AgentFlow 澄清结论"
	block := heading + "\n\n" + summary
	desc := strings.TrimRight(description, " \t\n")
	if i := strings.Index(desc, heading); i >= 0 {
		desc = strings.TrimRight(desc[:i], " \t\n")
	}
	if desc == "" {
		return block
	}
	return desc + "\n\n" + block
}

func clarificationComment(issue domain.Issue, summary string) string {
	source := "工作项"
	if issue.Source == "gongfeng" {
		source = "工蜂 Issue"
	} else if issue.Source == "tapd" {
		source = "TAPD 需求"
	}
	return fmt.Sprintf("## AgentFlow 澄清结论\n\n%s\n\n---\n来源：%s `%s`，由 AgentFlow 基于当前描述和用户回答生成。", strings.TrimSpace(summary), source, issue.ID)
}

func assessClarification(policy domain.ClarificationPolicy, answers map[string]string) domain.ClarificationAssessment {
	return domain.AssessClarificationAnswers(policy, answers)
}

func (h issuesHandlers) bindIssueRepositories(v *domain.Issue) error {
	branch, err := normalizeIssueBaseBranch(v.BaseBranch)
	if err != nil {
		return err
	}
	v.BaseBranch = branch
	ids := v.EffectiveRepositoryIDs()
	if len(ids) == 0 {
		if project, ok := h.repo.Project(v.ProjectID); ok && strings.TrimSpace(project.DefaultRepositoryID) != "" {
			ids = []string{project.DefaultRepositoryID}
		}
	}
	repositories := make([]domain.Repository, 0, len(ids))
	for _, id := range ids {
		repository, ok := h.repo.Repository(id)
		if !ok || repository.ProjectID != v.ProjectID {
			return fmt.Errorf("repository must belong to issue project")
		}
		repositories = append(repositories, repository)
	}
	if err := validateIssueBaseBranchRepositories(branch, repositories); err != nil {
		return err
	}
	if len(ids) > 0 {
		v.RepositoryIDs = ids
		v.RepositoryID = ids[0]
	}
	return nil
}

type issueListQuery struct {
	ProjectID string
	Status    string
	Source    string
	Q         string
	Assignee  string
}

func filterIssues(items []domain.Issue, q issueListQuery) []domain.Issue {
	out := make([]domain.Issue, 0, len(items))
	for _, item := range items {
		if q.ProjectID != "" && item.ProjectID != q.ProjectID {
			continue
		}
		if !csvContainsFold(q.Status, item.Status) {
			continue
		}
		if !csvContainsFold(q.Source, item.Source) {
			continue
		}
		if q.Assignee != "" {
			if strings.EqualFold(q.Assignee, "unassigned") {
				if item.AssigneeUserID != 0 || strings.TrimSpace(item.AssigneeID) != "" {
					continue
				}
			} else {
				matched := false
				if item.AssigneeUserID > 0 && strconv.FormatInt(item.AssigneeUserID, 10) == q.Assignee {
					matched = true
				} else if strings.EqualFold(item.AssigneeID, q.Assignee) {
					matched = true
				}
				if !matched {
					continue
				}
			}
		}
		if q.Q != "" {
			needle := strings.ToLower(strings.TrimSpace(q.Q))
			hay := strings.ToLower(item.Title + " " + item.ID + " " + strconv.Itoa(item.Number) + " " + item.RequirementTAPDID())
			if item.ExternalRef != nil {
				hay += " " + strings.ToLower(item.ExternalRef.ExternalID)
			}
			if !strings.Contains(hay, needle) && !fuzzyIssueTitleMatch(item.Title, needle) {
				continue
			}
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		if out[i].Number != out[j].Number {
			return out[i].Number > out[j].Number
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func fuzzyIssueTitleMatch(title, query string) bool {
	normalizedTitle := normalizeIssueTitleSearch(title)
	normalizedQuery := normalizeIssueTitleSearch(query)
	if normalizedQuery != "" && strings.Contains(normalizedTitle, normalizedQuery) {
		return true
	}
	tokens := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		if normalized := normalizeIssueTitleSearch(token); normalized == "" || !strings.Contains(normalizedTitle, normalized) {
			return false
		}
	}
	return true
}

func normalizeIssueTitleSearch(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func csvContainsFold(csv, value string) bool {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return true
	}
	value = strings.ToLower(strings.TrimSpace(value))
	for _, part := range strings.Split(csv, ",") {
		if strings.ToLower(strings.TrimSpace(part)) == value {
			return true
		}
	}
	return false
}

func parseIssueListInt(raw string, defaultVal int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultVal, nil
	}
	return strconv.Atoi(raw)
}

func keepExistingTrackerBinding(v *domain.Issue, existing domain.Issue) {
	repo.RestoreTrackerBinding(v, existing)
}

func (h issuesHandlers) normalizeIssueSOPBindings(projectID string, in []domain.IssueSOPBinding) ([]domain.IssueSOPBinding, error) {
	return normalizeSOPBindings(h.repo, projectID, in)
}

func normalizeSOPBindings(store *repo.Store, projectID string, in []domain.IssueSOPBinding) ([]domain.IssueSOPBinding, error) {
	bindings := domain.NormalizeSOPBindings(in)
	for i, binding := range bindings {
		if !knownWorkflow(store, binding.WorkflowID) {
			return nil, fmt.Errorf("SOP %s 不存在", binding.WorkflowID)
		}
		if binding.AssigneeUserID > 0 {
			if _, ok := store.User(binding.AssigneeUserID); !ok {
				return nil, fmt.Errorf("步骤责任人必须是有效用户")
			}
		}
		if wf, ok := store.Workflow(binding.WorkflowID); ok && strings.TrimSpace(wf.ProjectID) != "" && wf.ProjectID != projectID && !wf.Shared && !wf.Generic {
			return nil, fmt.Errorf("SOP %s 不属于当前项目", binding.WorkflowID)
		}
		bindings[i] = binding
	}
	return bindings, nil
}

func (h issuesHandlers) bindRequirementPlaybook(issue *domain.Issue) {
	if issue == nil || !domain.IsRequirementWorkItem(*issue) {
		return
	}
	project, ok := h.repo.Project(issue.ProjectID)
	if !ok {
		return
	}
	if id := strings.TrimSpace(issue.PlaybookID); id != "" {
		if _, found := project.Playbook(id); !found {
			issue.PlaybookID = ""
		}
	}
	if strings.TrimSpace(issue.PlaybookID) == "" {
		if book, found := project.DefaultPlaybook(); found {
			issue.PlaybookID = book.ID
		}
	}
	book, found := project.Playbook(issue.PlaybookID)
	if !found {
		book, found = project.DefaultPlaybook()
	}
	if !found || len(book.Bindings) == 0 {
		return
	}
	if strings.TrimSpace(issue.WorkflowID) == "" {
		issue.WorkflowID = book.Bindings[0].WorkflowID
	}
}

func (h issuesHandlers) assignIssueWorkflow(issue *domain.Issue, workflowID string) error {
	if issue == nil {
		return nil
	}
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil
	}
	if !knownWorkflow(h.repo, workflowID) {
		return fmt.Errorf("workflow not found")
	}
	issue.WorkflowID = workflowID
	return nil
}

func (h issuesHandlers) knownWorkflow(id string) bool {
	return knownWorkflow(h.repo, id)
}

func knownWorkflow(store *repo.Store, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if _, ok := store.Workflow(id); ok {
		return true
	}
	return id == repo.DefaultWorkflowID("tapd", "requirement") || id == repo.DefaultWorkflowID("gongfeng", "issue")
}

func (h issuesHandlers) createIssueOnTracker(v domain.Issue, target string) (domain.Issue, error) {
	if v.ID == "" {
		v.ID = fmt.Sprintf("issue-%d", time.Now().UnixNano())
	}
	h.configureSync(h.repo.SyncConfig())
	switch target {
	case "gongfeng":
		if v.RepositoryID == "" {
			return v, fmt.Errorf("同步到工蜂 Issue 需要先绑定代码仓库")
		}
		return h.sync.CreateGongfengIssue(h.repo, v)
	case "tapd":
		return h.sync.CreateTAPDStory(h.repo, v)
	default:
		return v, fmt.Errorf("syncTarget must be gongfeng or tapd")
	}
}

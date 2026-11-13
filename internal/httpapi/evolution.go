package httpapi

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"

	"github.com/gin-gonic/gin"
)

// evolutionHandlers serves the 进化中心: diagnosis findings, proposals with
// replay validation, release / reject / rollback of role prompts and rules,
// autonomy policies, run feedback and pinned regression samples.
type evolutionHandlers struct {
	*deps
	jobs    chan struct{}
	healthM sync.Mutex
	health  map[string]evoHealthCache
}

type evoHealthCache struct {
	at    time.Time
	days  int
	items []domain.EvoAssetHealth
}

const (
	evoDefaultWindowDays = 14
	evoMaxWindowDays     = 60
	evoMaxDiagnoseRuns   = 120
	evoHealthTTL         = 10 * time.Minute
	evoMaxConcurrentJobs = 2
)

func newEvolutionHandlers(d *deps) *evolutionHandlers {
	return &evolutionHandlers{deps: d, jobs: make(chan struct{}, evoMaxConcurrentJobs), health: map[string]evoHealthCache{}}
}

func registerEvolutionRoutes(g *gin.Engine, h *evolutionHandlers) {
	g.GET("/api/evolution/overview", h.overview)
	g.POST("/api/evolution/diagnose", h.diagnose)
	g.GET("/api/evolution/findings", h.findings)
	g.POST("/api/evolution/findings/:id/dismiss", h.dismissFinding)
	g.GET("/api/evolution/proposals", h.proposals)
	g.POST("/api/evolution/proposals", h.createProposal)
	g.GET("/api/evolution/proposals/:id", h.proposal)
	g.POST("/api/evolution/proposals/:id/approve", h.approveProposal)
	g.POST("/api/evolution/proposals/:id/reject", h.rejectProposal)
	g.GET("/api/evolution/policies", h.policies)
	g.PUT("/api/evolution/policies/:kind/:id", h.putPolicy)
	g.GET("/api/evolution/assets/:kind/:id/versions", h.assetVersions)
	g.POST("/api/evolution/assets/:kind/:id/rollback", h.rollbackAsset)
	g.GET("/api/evolution/samples", h.samples)
	g.POST("/api/runs/:id/feedback", h.runFeedback)
	g.POST("/api/runs/:id/pin-sample", h.pinSample)
	g.DELETE("/api/runs/:id/pin-sample", h.pinSample)
	g.GET("/api/runs/:id/asset-pins", h.runAssetPins)
}

// ---------- access ----------

// requireProjectReader allows platform admins, ownerless projects, the
// owner and every member (viewers included).
func (h *evolutionHandlers) requireProjectReader(c *gin.Context, projectID string) bool {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先选择项目"})
		return false
	}
	p, found := h.repo.Project(projectID)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
		return false
	}
	if !authRequired() || h.isPlatformAdmin(c.Request) {
		return true
	}
	user, ok := h.requestUser(c.Request, true)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
		return false
	}
	if p.OwnerID == 0 || p.OwnerID == user.ID {
		return true
	}
	for _, member := range p.Members {
		if member.UserID == user.ID {
			return true
		}
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "只有项目成员可以查看该项目的进化中心"})
	return false
}

// requireAssetWriter: platform-wide or shared assets need a platform admin,
// project assets need a project writer.
func (h *evolutionHandlers) requireAssetWriter(c *gin.Context, asset evoAsset) bool {
	if asset.Global {
		return h.requirePlatformAdmin(c)
	}
	return h.requireProjectWriter(c, asset.ProjectID)
}

func (h *evolutionHandlers) currentUserID(c *gin.Context) int64 {
	if user, ok := h.requestUser(c.Request, true); ok {
		return user.ID
	}
	return 0
}

// ---------- assets ----------

type evoAsset struct {
	Kind      string
	ID        string
	Name      string
	ProjectID string
	Global    bool
	Content   string
	agent     domain.AgentDefinition
	rule      domain.RuleDefinition
}

func (h *evolutionHandlers) loadAsset(kind, id string) (evoAsset, bool) {
	kind, id = strings.TrimSpace(kind), strings.TrimSpace(id)
	switch kind {
	case domain.EvoAssetAgent:
		agent, ok := h.repo.Agent(id)
		if !ok {
			return evoAsset{}, false
		}
		return evoAsset{Kind: kind, ID: agent.ID, Name: agent.Name, ProjectID: agent.ProjectID, Global: agent.ProjectID == "" || agent.Shared, Content: agent.Prompt, agent: agent}, true
	case domain.EvoAssetRule:
		rule, ok := h.repo.Rule(id)
		if !ok {
			return evoAsset{}, false
		}
		return evoAsset{Kind: kind, ID: rule.ID, Name: rule.Name, ProjectID: rule.ProjectID, Global: rule.ProjectID == "" || rule.Shared, Content: rule.Content, rule: rule}, true
	}
	return evoAsset{}, false
}

// writeAssetContent writes a new body into the live catalog asset.
func (h *evolutionHandlers) writeAssetContent(asset evoAsset, content string) error {
	switch asset.Kind {
	case domain.EvoAssetAgent:
		agent := asset.agent
		agent.Prompt = content
		return h.repo.PutAgent(agent)
	case domain.EvoAssetRule:
		rule := asset.rule
		rule.Content = content
		return h.repo.PutRule(rule)
	}
	return errUnsupportedEvoAsset
}

// ensureVersioned records the live body as a version when the version log
// does not end with it: baseline for the first touch, manual for edits made
// outside the evolution flow.
func (h *evolutionHandlers) ensureVersioned(asset evoAsset, userID int64) (domain.AssetVersion, error) {
	hash := domain.ContentHash(asset.Content)
	latest, ok := h.repo.LatestAssetVersion(asset.Kind, asset.ID)
	if ok && latest.Hash == hash {
		return latest, nil
	}
	source, note := domain.EvoVersionBaseline, "进化前的基线"
	if ok {
		source, note = domain.EvoVersionManual, "进化流程外的手动修改"
	}
	return h.repo.AppendAssetVersion(domain.AssetVersion{AssetKind: asset.Kind, AssetID: asset.ID, Content: asset.Content, Source: source, Note: note, CreatedBy: userID})
}

// ---------- overview & findings ----------

func evoWindowDays(raw string) int {
	days, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || days <= 0 {
		return evoDefaultWindowDays
	}
	return min(days, evoMaxWindowDays)
}

func (h *evolutionHandlers) overview(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if !h.requireProjectReader(c, projectID) {
		return
	}
	days := evoWindowDays(c.Query("days"))
	out := domain.EvoOverview{ProjectID: projectID, WindowDays: days, GeneratedAt: time.Now().UTC()}
	out.Findings = h.repo.EvoFindings(projectID, domain.EvoFindingOpen)
	out.Validating = evoProposalListView(h.repo.EvoProposals(projectID, 50, domain.EvoProposalDrafting, domain.EvoProposalValidating))
	out.Pending = evoProposalListView(h.repo.EvoProposals(projectID, 50, domain.EvoProposalPending))
	out.Recent = evoProposalListView(h.repo.EvoProposals(projectID, 12, domain.EvoProposalReleased, domain.EvoProposalRolledBack, domain.EvoProposalRejected, domain.EvoProposalFailed))
	out.Policies = h.repo.EvoPolicies(projectID)
	out.Health = h.cachedHealth(projectID, days, false)
	for i := range out.Health {
		for _, f := range out.Findings {
			if f.AssetKind == out.Health[i].AssetKind && f.AssetID == out.Health[i].AssetID {
				out.Health[i].OpenFindings++
			}
		}
	}
	c.JSON(http.StatusOK, out)
}

func (h *evolutionHandlers) findings(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if !h.requireProjectReader(c, projectID) {
		return
	}
	var statuses []string
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		statuses = strings.Split(status, ",")
	}
	c.JSON(http.StatusOK, h.repo.EvoFindings(projectID, statuses...))
}

func (h *evolutionHandlers) dismissFinding(c *gin.Context) {
	finding, ok := h.repo.EvoFinding(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "问题不存在"})
		return
	}
	if !h.requireProjectWriter(c, finding.ProjectID) {
		return
	}
	finding.Status = domain.EvoFindingDismissed
	if err := h.repo.PutEvoFinding(finding); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, finding)
}

// loadWindowRuns returns full states of the project's terminal business runs
// in the window, newest first. Replays are already excluded by the store.
func (h *evolutionHandlers) loadWindowRuns(projectID string, days, limit int) []domain.Run {
	summaries, err := h.repo.RunSummariesFor(domain.AnalyticsFilter{ProjectID: projectID, From: time.Now().UTC().AddDate(0, 0, -days)}, "")
	if err != nil {
		return nil
	}
	sort.SliceStable(summaries, func(i, j int) bool { return summaries[i].CreatedAt.After(summaries[j].CreatedAt) })
	out := make([]domain.Run, 0, min(len(summaries), limit))
	for _, s := range summaries {
		if len(out) >= limit {
			break
		}
		if s.IsWorkspace() || !domain.TerminalRunStatus(s.Status) || s.ExecutionContext.IsEvolutionReplay() {
			continue
		}
		if s.ExecutionContext.ProjectID != "" && s.ExecutionContext.ProjectID != projectID {
			continue
		}
		run, ok := h.repo.RunState(s.ID)
		if !ok {
			continue
		}
		out = append(out, run)
	}
	return out
}

// evolvableAgent resolves a stored role the evolution flow may rewrite.
// Generic templates (and stored rows shadowing one outside its project)
// are not evolvable in P1.
func (h *evolutionHandlers) evolvableAgent(wf domain.WorkflowDefinition, id string) (domain.AgentDefinition, bool) {
	if domain.IsPreviewBuildAgent(id) {
		return domain.AgentDefinition{}, false
	}
	agent, ok := h.repo.Agent(id)
	if !ok || h.repo.AgentShadowsTemplateOutside(agent, wf.ProjectID) {
		return domain.AgentDefinition{}, false
	}
	return agent, true
}

func (h *evolutionHandlers) diagnose(c *gin.Context) {
	var req struct {
		ProjectID string `json:"projectId"`
		Days      int    `json:"days"`
	}
	if !decode(c, &req) {
		return
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if !h.requireProjectReader(c, req.ProjectID) || !h.requireProjectWriter(c, req.ProjectID) {
		return
	}
	days := evoWindowDays(strconv.Itoa(req.Days))
	runs := h.loadWindowRuns(req.ProjectID, days, evoMaxDiagnoseRuns)
	evals := map[string]domain.RunEvalRecord{}
	for _, run := range runs {
		if rec, ok := h.repo.LatestRunEval(run.ID); ok && rec.Status == domain.RunEvalSucceeded {
			evals[run.ID] = rec
		}
	}
	found := evolution.Diagnose(evolution.DiagnoseInput{
		ProjectID: req.ProjectID,
		Runs:      runs,
		Evals:     evals,
		Feedback:  h.repo.EvoSignals(req.ProjectID, time.Now().UTC().AddDate(0, 0, -days)),
		Agent:     h.evolvableAgent,
		Rule:      h.repo.Rule,
		Level:     h.repo.EvoLevel,
	})
	saved := make([]domain.EvoFinding, 0, len(found))
	for _, f := range found {
		if existing, ok := h.repo.OpenEvoFindingForAsset(req.ProjectID, f.AssetKind, f.AssetID); ok {
			f.ID, f.CreatedAt = existing.ID, existing.CreatedAt
		} else {
			f.ID = "evf-" + randomID()
		}
		if p, open := h.repo.OpenEvoProposalForAsset(f.AssetKind, f.AssetID); open {
			f.ProposalID = p.ID
		}
		if err := h.repo.PutEvoFinding(f); err != nil {
			respond(c, err)
			return
		}
		saved = append(saved, f)
	}
	h.storeHealth(req.ProjectID, days, computeEvoHealth(runs, h.evolvableAgent, h.repo.Rule, h.repo.EvoLevel, h.latestVersion))
	c.JSON(http.StatusOK, gin.H{"findings": saved, "runs": len(runs), "windowDays": days})
}

func (h *evolutionHandlers) latestVersion(kind, id string) int {
	if v, ok := h.repo.LatestAssetVersion(kind, id); ok {
		return v.Version
	}
	return 0
}

func (h *evolutionHandlers) storeHealth(projectID string, days int, items []domain.EvoAssetHealth) {
	h.healthM.Lock()
	defer h.healthM.Unlock()
	h.health[projectID] = evoHealthCache{at: time.Now(), days: days, items: items}
}

func (h *evolutionHandlers) cachedHealth(projectID string, days int, force bool) []domain.EvoAssetHealth {
	h.healthM.Lock()
	cached, ok := h.health[projectID]
	h.healthM.Unlock()
	if ok && !force && cached.days == days && time.Since(cached.at) < evoHealthTTL {
		return append([]domain.EvoAssetHealth(nil), cached.items...)
	}
	runs := h.loadWindowRuns(projectID, days, 80)
	items := computeEvoHealth(runs, h.evolvableAgent, h.repo.Rule, h.repo.EvoLevel, h.latestVersion)
	h.storeHealth(projectID, days, items)
	return append([]domain.EvoAssetHealth(nil), items...)
}

// computeEvoHealth tallies how often each evolvable role / rule took part
// in a run of the window, and how those runs ended.
func computeEvoHealth(runs []domain.Run, agentOf func(domain.WorkflowDefinition, string) (domain.AgentDefinition, bool), ruleOf func(string) (domain.RuleDefinition, bool), level func(string, string) int, latest func(string, string) int) []domain.EvoAssetHealth {
	byKey := map[string]*domain.EvoAssetHealth{}
	order := []string{}
	touch := func(kind, id, name string) *domain.EvoAssetHealth {
		key := kind + ":" + id
		item := byKey[key]
		if item == nil {
			item = &domain.EvoAssetHealth{AssetKind: kind, AssetID: id, AssetName: name}
			byKey[key] = item
			order = append(order, key)
		}
		return item
	}
	for _, run := range runs {
		seen := map[string]bool{}
		wf := run.WorkflowSnapshot
		for _, node := range wf.Nodes {
			if node.Type == "script" || node.Type == "command" || node.Type == "approval" || node.Type == "sop" || node.AgentID == "" {
				continue
			}
			nr := run.Nodes[node.ID]
			if nr == nil || nr.Status == "pending" {
				continue
			}
			agent, ok := agentOf(wf, node.AgentID)
			if !ok {
				continue
			}
			item := touch(domain.EvoAssetAgent, agent.ID, agent.Name)
			key := domain.EvoAssetAgent + ":" + agent.ID
			if !seen[key] {
				seen[key] = true
				item.Runs++
				if run.Status == "succeeded" {
					item.Succeeded++
				}
			}
			if nr.Status == "failed" {
				item.Failed++
			}
			if nr.ReworkCount > 0 {
				item.Reworked++
			}
			for _, ruleID := range agent.Rules {
				rule, found := ruleOf(ruleID)
				if !found {
					continue
				}
				ruleItem := touch(domain.EvoAssetRule, rule.ID, rule.Name)
				ruleKey := domain.EvoAssetRule + ":" + rule.ID
				if !seen[ruleKey] {
					seen[ruleKey] = true
					ruleItem.Runs++
					if run.Status == "succeeded" {
						ruleItem.Succeeded++
					} else {
						ruleItem.Failed++
					}
				}
			}
		}
	}
	out := make([]domain.EvoAssetHealth, 0, len(order))
	for _, key := range order {
		item := *byKey[key]
		if item.Runs > 0 {
			item.SuccessRate = float64(item.Succeeded) / float64(item.Runs)
		}
		item.Level = level(item.AssetKind, item.AssetID)
		item.LatestVersion = latest(item.AssetKind, item.AssetID)
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SuccessRate != out[j].SuccessRate {
			return out[i].SuccessRate < out[j].SuccessRate
		}
		return out[i].Runs > out[j].Runs
	})
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// ---------- proposals ----------

// evoProposalView drops the per-replay candidate copies (the proposal itself
// carries the candidate); list views also drop both bodies.
func evoProposalView(p domain.EvoProposal, list bool) domain.EvoProposal {
	if len(p.Replays) > 0 {
		replays := make([]domain.EvoReplay, len(p.Replays))
		for i, r := range p.Replays {
			r.Overrides = domain.EvoAssetOverride{}
			replays[i] = r
		}
		p.Replays = replays
	}
	if list {
		p.BaseContent, p.CandidateContent = "", ""
	}
	return p
}

func evoProposalListView(items []domain.EvoProposal) []domain.EvoProposal {
	out := make([]domain.EvoProposal, len(items))
	for i, p := range items {
		out[i] = evoProposalView(p, true)
	}
	return out
}

func (h *evolutionHandlers) proposals(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if !h.requireProjectReader(c, projectID) {
		return
	}
	var statuses []string
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		statuses = strings.Split(status, ",")
	}
	c.JSON(http.StatusOK, evoProposalListView(h.repo.EvoProposals(projectID, 100, statuses...)))
}

func (h *evolutionHandlers) proposal(c *gin.Context) {
	p, ok := h.repo.EvoProposal(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "提案不存在"})
		return
	}
	if !h.requireProjectReader(c, p.ProjectID) {
		return
	}
	c.JSON(http.StatusOK, evoProposalView(p, false))
}

func (h *evolutionHandlers) createProposal(c *gin.Context) {
	var req struct {
		ProjectID string `json:"projectId"`
		FindingID string `json:"findingId"`
		AssetKind string `json:"assetKind"`
		AssetID   string `json:"assetId"`
		Model     string `json:"model"`
	}
	if !decode(c, &req) {
		return
	}
	var finding domain.EvoFinding
	if id := strings.TrimSpace(req.FindingID); id != "" {
		f, ok := h.repo.EvoFinding(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "问题不存在"})
			return
		}
		finding = f
		req.ProjectID, req.AssetKind, req.AssetID = f.ProjectID, f.AssetKind, f.AssetID
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if !h.requireProjectReader(c, req.ProjectID) || !h.requireProjectWriter(c, req.ProjectID) {
		return
	}
	asset, ok := h.loadAsset(req.AssetKind, req.AssetID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "资产不存在，或该类型暂不支持进化（P1 仅支持角色 Prompt 与规则）"})
		return
	}
	if !asset.Global && asset.ProjectID != req.ProjectID {
		c.JSON(http.StatusForbidden, gin.H{"error": "该资产不属于当前项目"})
		return
	}
	if level := h.repo.EvoLevel(asset.Kind, asset.ID); level < domain.EvoLevelPropose {
		c.JSON(http.StatusConflict, gin.H{"error": "该资产的进化级别不允许生成提案，请先在策略中调到 L2"})
		return
	}
	if open, exists := h.repo.OpenEvoProposalForAsset(asset.Kind, asset.ID); exists {
		c.JSON(http.StatusConflict, gin.H{"error": "该资产已有进行中的提案", "proposalId": open.ID})
		return
	}
	current, loggedIn := h.requestUser(c.Request, true)
	if !loggedIn {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录后再生成提案"})
		return
	}
	user, ok := h.repo.User(current.ID)
	if !ok || strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先在个人配置中配置自己的 CodeBuddy API Key"})
		return
	}
	if finding.ID == "" {
		f, found := h.findingForAsset(req.ProjectID, asset)
		if !found {
			c.JSON(http.StatusConflict, gin.H{"error": "近 14 天没有该资产的失败、打回或反馈信号，没有可用来验证的样本"})
			return
		}
		finding = f
	}
	catalog := loadAssistantModels(c.Request.Context(), user.CodeBuddyInternetEnvironment)
	p := domain.EvoProposal{
		ID:          "evp-" + randomID(),
		ProjectID:   req.ProjectID,
		FindingID:   finding.ID,
		AssetKind:   asset.Kind,
		AssetID:     asset.ID,
		AssetName:   asset.Name,
		Title:       "正在生成改进提案…",
		BaseContent: asset.Content,
		BaseHash:    domain.ContentHash(asset.Content),
		Status:      domain.EvoProposalDrafting,
		Model:       assistantAllowedModel(req.Model, catalog),
		CreatedBy:   user.ID,
	}
	if err := h.repo.PutEvoProposal(p); err != nil {
		respond(c, err)
		return
	}
	if finding.ID != "" {
		finding.ProposalID = p.ID
		finding.Status = domain.EvoFindingProposed
		_ = h.repo.PutEvoFinding(finding)
	}
	h.launchProposalJob(p, finding, user)
	c.JSON(http.StatusAccepted, p)
}

func (h *evolutionHandlers) approveProposal(c *gin.Context) {
	var req struct {
		Note  string `json:"note"`
		Force bool   `json:"force"`
	}
	if !decodeOptional(c, &req) {
		return
	}
	p, ok := h.repo.EvoProposal(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "提案不存在"})
		return
	}
	asset, ok := h.loadAsset(p.AssetKind, p.AssetID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "资产已删除"})
		return
	}
	if !h.requireProjectWriter(c, p.ProjectID) || !h.requireAssetWriter(c, asset) {
		return
	}
	released, err := h.releaseProposal(p, asset, h.currentUserID(c), strings.TrimSpace(req.Note), req.Force && (!authRequired() || h.isPlatformAdmin(c.Request)))
	if err != nil {
		respondEvoError(c, err)
		return
	}
	c.JSON(http.StatusOK, evoProposalView(released, false))
}

func (h *evolutionHandlers) rejectProposal(c *gin.Context) {
	var req struct {
		Note string `json:"note"`
	}
	if !decodeOptional(c, &req) {
		return
	}
	p, ok := h.repo.EvoProposal(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "提案不存在"})
		return
	}
	if !h.requireProjectWriter(c, p.ProjectID) {
		return
	}
	if !p.Open() {
		c.JSON(http.StatusConflict, gin.H{"error": "提案已结束，不能打回"})
		return
	}
	h.cancelReplays(p)
	p.Status = domain.EvoProposalRejected
	p.DecidedBy = h.currentUserID(c)
	p.DecidedAt = time.Now().UTC()
	p.DecisionNote = strings.TrimSpace(req.Note)
	if err := h.repo.PutEvoProposal(p); err != nil {
		respond(c, err)
		return
	}
	h.reopenFinding(p)
	stored, _ := h.repo.EvoProposal(p.ID)
	c.JSON(http.StatusOK, evoProposalView(stored, false))
}

// reopenFinding puts the finding back on the board after a proposal ends
// without a release, so the owner can try again.
func (h *evolutionHandlers) reopenFinding(p domain.EvoProposal) {
	if p.FindingID == "" {
		return
	}
	if f, ok := h.repo.EvoFinding(p.FindingID); ok && f.ProposalID == p.ID {
		f.Status = domain.EvoFindingOpen
		f.ProposalID = ""
		_ = h.repo.PutEvoFinding(f)
	}
}

// ---------- policies ----------

func (h *evolutionHandlers) policies(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if !h.requireProjectReader(c, projectID) {
		return
	}
	c.JSON(http.StatusOK, h.repo.EvoPolicies(projectID))
}

func (h *evolutionHandlers) putPolicy(c *gin.Context) {
	var req struct {
		Level int `json:"level"`
	}
	if !decode(c, &req) {
		return
	}
	asset, ok := h.loadAsset(c.Param("kind"), c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "资产不存在"})
		return
	}
	if !h.requireAssetWriter(c, asset) {
		return
	}
	policy := domain.EvoPolicy{AssetKind: asset.Kind, AssetID: asset.ID, ProjectID: asset.ProjectID, Level: req.Level, UpdatedBy: h.currentUserID(c), UpdatedAt: time.Now().UTC()}
	if err := h.repo.PutEvoPolicy(policy); err != nil {
		respond(c, err)
		return
	}
	stored, _ := h.repo.EvoPolicy(asset.Kind, asset.ID)
	c.JSON(http.StatusOK, stored)
}

// ---------- versions & rollback ----------

func (h *evolutionHandlers) assetReadable(c *gin.Context, asset evoAsset) bool {
	if asset.Global {
		if !authRequired() {
			return true
		}
		if _, ok := h.requestUser(c.Request, true); !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
			return false
		}
		return true
	}
	return h.requireProjectReader(c, asset.ProjectID)
}

func (h *evolutionHandlers) assetVersions(c *gin.Context) {
	asset, ok := h.loadAsset(c.Param("kind"), c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "资产不存在"})
		return
	}
	if !h.assetReadable(c, asset) {
		return
	}
	hash := domain.ContentHash(asset.Content)
	versions := h.repo.AssetVersions(asset.Kind, asset.ID)
	current := 0
	for i := range versions {
		if versions[i].Hash == hash && current == 0 {
			versions[i].Current = true
			current = versions[i].Version
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"assetKind": asset.Kind, "assetId": asset.ID, "assetName": asset.Name,
		"liveHash": hash, "liveContent": asset.Content, "currentVersion": current,
		"drifted":  len(versions) > 0 && current == 0,
		"level":    h.repo.EvoLevel(asset.Kind, asset.ID),
		"versions": versions,
	})
}

func (h *evolutionHandlers) rollbackAsset(c *gin.Context) {
	var req struct {
		Version int    `json:"version"`
		Note    string `json:"note"`
	}
	if !decode(c, &req) {
		return
	}
	asset, ok := h.loadAsset(c.Param("kind"), c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "资产不存在"})
		return
	}
	if !h.requireAssetWriter(c, asset) {
		return
	}
	target, ok := h.repo.AssetVersion(asset.Kind, asset.ID, req.Version)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "版本不存在"})
		return
	}
	if domain.ContentHash(asset.Content) == target.Hash {
		c.JSON(http.StatusConflict, gin.H{"error": "当前已是该版本的内容"})
		return
	}
	if open, exists := h.repo.OpenEvoProposalForAsset(asset.Kind, asset.ID); exists {
		c.JSON(http.StatusConflict, gin.H{"error": "该资产有进行中的提案，请先打回再回滚", "proposalId": open.ID})
		return
	}
	userID := h.currentUserID(c)
	if _, err := h.ensureVersioned(asset, userID); err != nil {
		respond(c, err)
		return
	}
	if err := h.writeAssetContent(asset, target.Content); err != nil {
		respond(c, err)
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		note = "回滚到 v" + strconv.Itoa(target.Version)
	}
	version, err := h.repo.AppendAssetVersion(domain.AssetVersion{AssetKind: asset.Kind, AssetID: asset.ID, Content: target.Content, Source: domain.EvoVersionRollback, Note: note, CreatedBy: userID})
	if err != nil {
		respond(c, err)
		return
	}
	for _, p := range h.repo.EvoProposals("", 200, domain.EvoProposalReleased) {
		if p.AssetKind == asset.Kind && p.AssetID == asset.ID && p.ReleasedVersion > target.Version {
			p.Status = domain.EvoProposalRolledBack
			p.DecisionNote = strings.TrimSpace(p.DecisionNote + "\n已回滚：" + note)
			_ = h.repo.PutEvoProposal(p)
		}
	}
	c.JSON(http.StatusOK, version)
}

// ---------- run feedback & samples ----------

func (h *evolutionHandlers) runProject(run domain.Run) string {
	issueProject := ""
	if issue, found := h.repo.Issue(run.ExecutionContext.IssueID); found {
		issueProject = issue.ProjectID
	}
	return domain.RunProjectID(run, issueProject)
}

func (h *evolutionHandlers) runFeedback(c *gin.Context) {
	var req struct {
		NodeID string `json:"nodeId"`
		Detail string `json:"detail"`
	}
	if !decode(c, &req) {
		return
	}
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	if run.ExecutionContext.IsEvolutionReplay() {
		c.JSON(http.StatusConflict, gin.H{"error": "回放运行不收集反馈"})
		return
	}
	detail := strings.TrimSpace(req.Detail)
	if detail == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请写下哪里不好"})
		return
	}
	projectID := h.runProject(run)
	if projectID == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "该运行没有所属项目"})
		return
	}
	if !h.requireProjectReader(c, projectID) {
		return
	}
	sig := domain.EvoSignal{ID: "evs-" + randomID(), ProjectID: projectID, RunID: run.ID, NodeID: strings.TrimSpace(req.NodeID), Kind: evolution.SignalUserFeedback, Detail: domain.TrimEvoText(detail, 2000), UserID: h.currentUserID(c), CreatedAt: time.Now().UTC()}
	if err := h.repo.AddEvoSignal(sig); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, sig)
}

func (h *evolutionHandlers) pinSample(c *gin.Context) {
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	projectID := h.runProject(run)
	if projectID == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "该运行没有所属项目"})
		return
	}
	if !h.requireProjectReader(c, projectID) || !h.requireProjectWriter(c, projectID) {
		return
	}
	if c.Request.Method == http.MethodDelete {
		if err := h.repo.DeleteEvoSample(projectID, run.ID); err != nil {
			respond(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "deleted"})
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if !decodeOptional(c, &req) {
		return
	}
	if run.ExecutionContext.IsEvolutionReplay() || run.Status != "succeeded" {
		c.JSON(http.StatusConflict, gin.H{"error": "只能把成功的业务运行设为回归样本"})
		return
	}
	sample := domain.EvoSample{ProjectID: projectID, RunID: run.ID, WorkflowID: run.WorkflowID, Note: domain.TrimEvoText(req.Note, 500), CreatedBy: h.currentUserID(c), CreatedAt: time.Now().UTC()}
	if err := h.repo.PutEvoSample(sample); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusCreated, sample)
}

func (h *evolutionHandlers) samples(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if !h.requireProjectReader(c, projectID) {
		return
	}
	c.JSON(http.StatusOK, h.repo.EvoSamples(projectID))
}

func (h *evolutionHandlers) runAssetPins(c *gin.Context) {
	run, ok := h.repo.RunState(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	if projectID := h.runProject(run); projectID != "" && !h.requireProjectReader(c, projectID) {
		return
	}
	c.JSON(http.StatusOK, h.repo.RunAssetPins(run.ID))
}

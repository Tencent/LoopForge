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

// evolutionHandlers serves the RSI工作台: diagnosis findings, proposals with
// replay validation, release / reject / rollback of role prompts and rules,
// autonomy policies, run feedback and pinned regression samples.
type evolutionHandlers struct {
	*deps
	jobs chan struct{}
	// Serialize feedback materialization and explicit diagnosis reconciliation.
	findingsM sync.Mutex
	healthM   sync.Mutex
	health    map[string]evoHealthCache
	// trial / watch sweep throttle and per-run score cache
	trialM      sync.Mutex
	trialAt     time.Time
	trialScores map[string]int
	sopBatchM  sync.Mutex
	sopBatchAt time.Time
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
	evoMaxSnoozeDays     = 30
)

func newEvolutionHandlers(d *deps) *evolutionHandlers {
	return &evolutionHandlers{deps: d, jobs: make(chan struct{}, evoMaxConcurrentJobs), health: map[string]evoHealthCache{}, trialScores: map[string]int{}}
}

func registerEvolutionRoutes(g *gin.Engine, h *evolutionHandlers) {
	g.GET("/api/evolution/overview", h.overview)
	g.POST("/api/evolution/diagnose", h.diagnose)
	g.PUT("/api/evolution/sop-detection", h.putSOPDetection)
	g.GET("/api/evolution/sops", h.listSOPs)
	g.GET("/api/evolution/sops/:workflowId", h.sopDetail)
	g.PUT("/api/evolution/sops/:workflowId/policy", h.putSOPPolicy)
	g.POST("/api/evolution/sops/:workflowId/optimize", h.optimizeSOP)
	g.GET("/api/evolution/findings", h.findings)
	g.GET("/api/evolution/findings/:id/runs", h.findingRuns)
	g.POST("/api/evolution/findings/:id/dismiss", h.dismissFinding)
	g.POST("/api/evolution/findings/:id/snooze", h.snoozeFinding)
	g.GET("/api/evolution/proposals", h.proposals)
	g.POST("/api/evolution/proposals", h.createProposal)
	g.POST("/api/evolution/queue/:id/start", h.startQueue)
	g.POST("/api/evolution/queue/:id/reject", h.rejectQueue)
	g.GET("/api/evolution/proposals/:id", h.proposal)
	g.GET("/api/evolution/proposals/:id/replays/:rid/compare", h.replayCompare)
	g.POST("/api/evolution/proposals/:id/approve", h.approveProposal)
	g.POST("/api/evolution/proposals/:id/canary/traffic", h.adjustCanaryTraffic)
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
	c.JSON(http.StatusForbidden, gin.H{"error": "只有项目成员可以查看该项目的RSI工作台"})
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
	out := domain.EvoOverview{ProjectID: projectID, WindowDays: days, GeneratedAt: time.Now().UTC(), AutoEnabled: evolutionAutoEnabled()}
	if project, ok := h.repo.Project(projectID); ok && project.SOPDetection != nil {
		out.SOPDetection = domain.EvoSOPDetectionSetting{Enabled: project.SOPDetection.Enabled, Model: project.SOPDetection.Model}
	}
	out.Findings = h.repo.EvoFindings(projectID, domain.EvoFindingOpen)
	out.Queue = h.repo.EvoQueue(projectID)
	out.Validating = evoProposalListView(h.repo.EvoProposals(projectID, 50, domain.EvoProposalDrafting, domain.EvoProposalValidating))
	out.Pending = evoProposalListView(h.repo.EvoProposals(projectID, 50, domain.EvoProposalPending))
	trial := h.repo.EvoProposals(projectID, 50, domain.EvoProposalCanary)
	inTrial := map[string]bool{}
	for _, p := range h.repo.EvoProposals(projectID, 50, domain.EvoProposalReleased) {
		if p.Watch.Active() {
			trial = append(trial, p)
			inTrial[p.ID] = true
		}
	}
	out.Trial = evoProposalListView(trial)
	recent := []domain.EvoProposal{}
	for _, p := range h.repo.EvoProposals(projectID, 12+len(inTrial), domain.EvoProposalReleased, domain.EvoProposalRolledBack, domain.EvoProposalRejected, domain.EvoProposalFailed) {
		if !inTrial[p.ID] && len(recent) < 12 {
			recent = append(recent, p)
		}
	}
	out.Recent = evoProposalListView(recent)
	out.Policies = h.repo.EvoPolicies(projectID)
	out.Health = h.cachedHealth(projectID, days, false)
	for i := range out.Health {
		for _, f := range out.Findings {
			if f.AssetKind == out.Health[i].AssetKind && f.AssetID == out.Health[i].AssetID {
				out.Health[i].OpenFindings++
			}
		}
	}
	out.SOPs = h.buildSOPSummaries(projectID, days)
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
	var req struct {
		Reason string `json:"reason"`
	}
	if !decodeOptional(c, &req) {
		return
	}
	finding, ok := h.repo.EvoFinding(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "问题不存在"})
		return
	}
	if !h.requireProjectWriter(c, finding.ProjectID) {
		return
	}
	finding.Status = domain.EvoFindingDismissed
	finding.CloseNote = domain.TrimEvoText(req.Reason, 500)
	if err := h.repo.PutEvoFinding(finding); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, finding)
}

// snoozeFinding keeps an open finding out of the active queue for a few
// days; days 0 wakes it up. New signals still update its statistics.
func (h *evolutionHandlers) snoozeFinding(c *gin.Context) {
	var req struct {
		Days int `json:"days"`
	}
	if !decodeOptional(c, &req) {
		return
	}
	finding, ok := h.repo.EvoFinding(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "问题不存在"})
		return
	}
	if !h.requireProjectWriter(c, finding.ProjectID) {
		return
	}
	if finding.Status != domain.EvoFindingOpen {
		c.JSON(http.StatusConflict, gin.H{"error": "只有待处理的问题可以暂缓"})
		return
	}
	finding.SnoozedUntil = time.Time{}
	if req.Days > 0 {
		finding.SnoozedUntil = time.Now().UTC().AddDate(0, 0, min(req.Days, evoMaxSnoozeDays))
	}
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
	h.findingsM.Lock()
	defer h.findingsM.Unlock()
	days := evoWindowDays(strconv.Itoa(req.Days))
	runs := h.loadWindowRuns(req.ProjectID, days, evoMaxDiagnoseRuns)
	found := evolution.Diagnose(h.diagnoseInput(req.ProjectID, days, runs, h.repo.EvoLevel))
	saved := make([]domain.EvoFinding, 0, len(found))
	keep := map[string]bool{}
	activeFindings := h.repo.EvoFindings(req.ProjectID, domain.EvoFindingOpen, domain.EvoFindingProposed)
	queuedFindings := h.repo.EvoQueue(req.ProjectID)
	for _, f := range found {
		f, err := h.saveDiagnosedFinding(f, activeFindings, queuedFindings)
		if err != nil {
			respond(c, err)
			return
		}
		keep[f.ID] = true
		saved = append(saved, f)
	}
	archived := 0
	for _, item := range h.repo.EvoQueue(req.ProjectID) {
		keep[item.FindingID] = true
	}
	if len(runs) > 0 {
		for _, f := range h.repo.EvoFindings(req.ProjectID, domain.EvoFindingOpen) {
			if keep[f.ID] {
				continue
			}
			f.Status = domain.EvoFindingResolved
			f.CloseNote = "近 " + strconv.Itoa(days) + " 天当前版本没有再出现问题，自动归档"
			if h.repo.PutEvoFinding(f) == nil {
				archived++
			}
		}
	}
	h.storeHealth(req.ProjectID, days, computeEvoHealth(runs, h.evolvableAgent, h.repo.Rule, h.repo.EvoLevel, h.latestVersion))
	c.JSON(http.StatusOK, gin.H{"findings": saved, "runs": len(runs), "windowDays": days, "archived": archived})
}

// diagnoseInput loads evals, feedback and version pins for a diagnosis.
func (h *evolutionHandlers) diagnoseInput(projectID string, days int, runs []domain.Run, level func(kind, id string) int) evolution.DiagnoseInput {
	evals := map[string]domain.RunEvalRecord{}
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
		if rec, ok := h.repo.LatestRunEval(run.ID); ok && rec.Status == domain.RunEvalSucceeded {
			evals[run.ID] = rec
		}
	}
	return evolution.DiagnoseInput{
		ProjectID:     projectID,
		Runs:          runs,
		Evals:         evals,
		SOPDetections: h.repo.SOPDetectionEvaluations(ids),
		Feedback:      h.repo.EvoSignals(projectID, time.Now().UTC().AddDate(0, 0, -days)),
		Agent:         h.evolvableAgent,
		Rule:          h.repo.Rule,
		Level:         level,
		WindowDays:    days,
		Pins:          h.repo.RunAssetPins,
	}
}

func evoFindingQueued(items []domain.EvoQueueItem, findingID string) bool {
	for _, item := range items {
		if item.FindingID == findingID {
			return true
		}
	}
	return false
}

func sameEvoFindingIntent(a, b domain.EvoFinding) bool {
	keys := func(f domain.EvoFinding) []string {
		set := map[string]bool{}
		for _, cluster := range f.Clusters {
			set[cluster.Kind+"|"+cluster.NodeID+"|"+cluster.Label] = true
		}
		if len(set) == 0 {
			for _, signal := range f.Signals {
				set[signal.Kind+"|"+signal.NodeID+"|"+signal.Detail] = true
			}
		}
		out := make([]string, 0, len(set))
		for key := range set {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	}
	ak, bk := keys(a), keys(b)
	return len(ak) > 0 && len(ak) == len(bk) && strings.Join(ak, "\n") == strings.Join(bk, "\n")
}

// mergeEvoFinding refreshes a tracked finding with a new diagnosis: same
// ID, first sighting and snooze; a finding already in a proposal stays so.
func mergeEvoFinding(f, existing domain.EvoFinding) domain.EvoFinding {
	f.ID, f.CreatedAt = existing.ID, existing.CreatedAt
	if existing.Status == domain.EvoFindingProposed {
		f.Status, f.ProposalID = existing.Status, existing.ProposalID
	}
	if !existing.FirstSeenAt.IsZero() && (f.FirstSeenAt.IsZero() || existing.FirstSeenAt.Before(f.FirstSeenAt)) {
		f.FirstSeenAt = existing.FirstSeenAt
	}
	if existing.SnoozedUntil.After(time.Now()) {
		f.SnoozedUntil = existing.SnoozedUntil
	}
	return f
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
			if agent.Shared || agent.ProjectID == "" {
				item.ReadOnlyReason = "共享或平台角色仅供诊断，请先复制为项目私有角色"
			}
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
				if rule.Shared || rule.ProjectID == "" {
					ruleItem.ReadOnlyReason = "共享或平台规则仅供诊断，请先复制为项目私有规则"
				}
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
	h.loadHistoricalReplayMeasurements(&p)
	c.JSON(http.StatusOK, evoProposalDetailResponse{EvoProposal: evoProposalView(p, false), ReleaseEvaluation: h.releaseEvaluation(p)})
}

func (h *evolutionHandlers) createProposal(c *gin.Context) {
	var req struct {
		ProjectID string `json:"projectId"`
		FindingID string `json:"findingId"`
		AssetKind string `json:"assetKind"`
		AssetID   string `json:"assetId"`
		Model     string `json:"model"`
	}
	startID := c.GetString("evoQueueStartID")
	if startID != "" {
		item, ok := h.repo.EvoQueueItem(startID)
		if !ok || item.Status != "queued" {
			c.JSON(http.StatusConflict, gin.H{"error": "待处理项已变化，请刷新"})
			return
		}
		req.FindingID, req.Model = item.FindingID, item.Model
	} else if !decode(c, &req) {
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
	if err := privateEvolutionAsset(asset, req.ProjectID); err != nil {
		respondEvoError(c, err)
		return
	}
	if level := h.repo.EvoLevel(asset.Kind, asset.ID); level < domain.EvoLevelPropose {
		c.JSON(http.StatusConflict, gin.H{"error": "该资产的进化级别不允许生成提案，请先在策略中调到 L2"})
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
	catalog := assistantModelsForRequest(c.Request.Context(), user, req.Model)
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
	p, queued, created, err := h.repo.SubmitEvoProposal(p, "evq-"+randomID(), startID)
	if err != nil {
		respondEvoError(c, err)
		return
	}
	if queued != nil {
		c.JSON(http.StatusAccepted, gin.H{"status": queued.Status, "queueId": queued.ID, "queue": queued})
		return
	}
	if !created {
		c.JSON(http.StatusOK, evoProposalView(p, false))
		return
	}
	h.launchProposalJob(p, finding, user)
	c.JSON(http.StatusAccepted, p)
}

// approveProposal releases a pending proposal or promotes a trial to every
// run. mode "canary" starts a small-traffic trial instead of releasing.
func (h *evolutionHandlers) approveProposal(c *gin.Context) {
	var req struct {
		Note    string `json:"note"`
		Force   bool   `json:"force"`
		Mode    string `json:"mode"`
		Percent int    `json:"percent"`
		MinRuns int    `json:"minRuns"`
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
	var (
		out domain.EvoProposal
		err error
	)
	mode := strings.TrimSpace(req.Mode)
	if mode != "" && mode != "canary" && mode != "release" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未知的发布模式"})
		return
	}
	if mode == "canary" {
		out, err = h.startCanary(p, asset, h.currentUserID(c), req.Percent, req.MinRuns, req.Note, false)
	} else {
		out, err = h.releaseProposal(p, asset, h.currentUserID(c), strings.TrimSpace(req.Note), req.Force && (!authRequired() || h.isPlatformAdmin(c.Request)))
	}
	if err != nil {
		respondEvoError(c, err)
		return
	}
	c.JSON(http.StatusOK, evoProposalView(out, false))
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
	p.Status = domain.EvoProposalRejected
	p.DecidedBy = h.currentUserID(c)
	p.DecidedAt = time.Now().UTC()
	p.DecisionNote = strings.TrimSpace(req.Note)
	if p.Canary != nil && p.Canary.EndedAt.IsZero() {
		p.Canary.Status, p.Canary.EndedAt = domain.EvoCanaryAborted, p.DecidedAt
		p.Canary.Verdict = strings.TrimSpace("人工终止试用。" + p.DecisionNote)
	}
	if err := h.repo.PutEvoProposal(p); err != nil {
		respond(c, err)
		return
	}
	h.cancelReplays(p)
	stored, _ := h.repo.EvoProposal(p.ID)
	c.JSON(http.StatusOK, evoProposalView(stored, false))
}

func (h *evolutionHandlers) startQueue(c *gin.Context) {
	item, ok := h.repo.EvoQueueItem(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "待处理项不存在"})
		return
	}
	if !h.requireProjectReader(c, item.ProjectID) || !h.requireProjectWriter(c, item.ProjectID) {
		return
	}
	c.Set("evoQueueStartID", item.ID)
	h.createProposal(c)
}

func (h *evolutionHandlers) rejectQueue(c *gin.Context) {
	item, ok := h.repo.EvoQueueItem(c.Param("id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "待处理项不存在"})
		return
	}
	if !h.requireProjectReader(c, item.ProjectID) || !h.requireProjectWriter(c, item.ProjectID) {
		return
	}
	out, err := h.repo.RejectEvoQueue(item.ID)
	if err != nil {
		respondEvoError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
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
	if req.Level >= domain.EvoLevelPropose {
		if err := privateEvolutionAsset(asset, asset.ProjectID); err != nil {
			respondEvoError(c, err)
			return
		}
	}
	if req.Level >= domain.EvoLevelAuto && !evolutionAutoEnabled() {
		c.JSON(http.StatusConflict, gin.H{"error": "自动进化尚未启用，当前可使用 L0–L2"})
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
		c.JSON(http.StatusConflict, gin.H{"error": "该资产有进行中的提案或试用，请先打回再回滚", "proposalId": open.ID})
		return
	}
	version, err := h.rollbackTo(asset, target, h.currentUserID(c), req.Note)
	if err != nil {
		respondEvoError(c, err)
		return
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
	c.JSON(http.StatusCreated, h.feedbackFinding(run, sig))
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

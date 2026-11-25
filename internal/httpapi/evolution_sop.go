package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"

	"github.com/gin-gonic/gin"
)

type assistantEditSourceContextKey struct{}

func catalogEditSource(ctx context.Context) (string, string) {
	if ctx != nil {
		if src, ok := ctx.Value(assistantEditSourceContextKey{}).(string); ok && src == domain.EvoVersionChatbot {
			return domain.EvoVersionChatbot, "通过 Chatbot 修改"
		}
	}
	return domain.EvoVersionManual, "手动修改"
}

const evoSOPWindowLimit = 200

func (h *evolutionHandlers) listSOPs(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	if !h.requireProjectReader(c, projectID) {
		return
	}
	days := evoWindowDays(c.Query("days"))
	c.JSON(http.StatusOK, h.buildSOPSummaries(projectID, days))
}

func (h *evolutionHandlers) sopDetail(c *gin.Context) {
	projectID := strings.TrimSpace(c.Query("projectId"))
	workflowID := strings.TrimSpace(c.Param("workflowId"))
	if !h.requireProjectReader(c, projectID) {
		return
	}
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 workflowId"})
		return
	}
	days := evoWindowDays(c.Query("days"))
	detail, ok := h.buildSOPDetail(projectID, workflowID, days)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "SOP 不存在"})
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (h *evolutionHandlers) putSOPPolicy(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflowId"))
	var req struct {
		ProjectID   string   `json:"projectId"`
		Enabled     *bool    `json:"enabled"`
		Preferences []string `json:"preferences"`
		BatchSize   *int     `json:"batchSize"`
		Model       *string  `json:"model"`
	}
	if !decode(c, &req) {
		return
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		req.ProjectID = strings.TrimSpace(c.Query("projectId"))
	}
	if !h.requireProjectReader(c, req.ProjectID) || !h.requireProjectWriter(c, req.ProjectID) {
		return
	}
	if workflowID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 workflowId"})
		return
	}
	cur := h.repo.EvoSOPPolicy(req.ProjectID, workflowID)
	cur.ProjectID = req.ProjectID
	cur.WorkflowID = workflowID
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if req.Preferences != nil {
		cur.Preferences = evolution.NormalizeSOPPreferences(req.Preferences)
	} else if len(cur.Preferences) == 0 {
		cur.Preferences = evolution.NormalizeSOPPreferences(nil)
	}
	if req.BatchSize != nil {
		cur.BatchSize = evolution.NormalizeSOPBatchSize(*req.BatchSize)
	} else {
		cur.BatchSize = evolution.NormalizeSOPBatchSize(cur.BatchSize)
	}
	if req.Model != nil {
		model := strings.TrimSpace(*req.Model)
		if model != "" {
			if current, loggedIn := h.requestUser(c.Request, true); loggedIn {
				if user, ok := h.repo.User(current.ID); ok {
					if resolved, err := resolveFeatureModel(user, model); err == nil && resolved != "" {
						model = resolved
					} else {
						catalog := assistantModelsForRequest(c.Request.Context(), user, model)
						if allowed := assistantAllowedModel(model, catalog); allowed != "" {
							model = allowed
						}
					}
				}
			}
		}
		cur.Model = model
	}
	cur.UpdatedBy = h.currentUserID(c)
	cur.UpdatedAt = time.Now().UTC()
	if err := h.repo.PutEvoSOPPolicy(cur); err != nil {
		respond(c, err)
		return
	}
	c.JSON(http.StatusOK, h.repo.EvoSOPPolicy(req.ProjectID, workflowID))
}

// optimizeSOP initiates an optimization proposal for the SOP's highest-impact evolvable asset.
func (h *evolutionHandlers) optimizeSOP(c *gin.Context) {
	workflowID := strings.TrimSpace(c.Param("workflowId"))
	var req struct {
		ProjectID string `json:"projectId"`
		Model     string `json:"model"`
	}
	if !decodeOptional(c, &req) {
		return
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		req.ProjectID = strings.TrimSpace(c.Query("projectId"))
	}
	if !h.requireProjectReader(c, req.ProjectID) || !h.requireProjectWriter(c, req.ProjectID) {
		return
	}
	wf, ok := h.resolveSOPWorkflow(req.ProjectID, workflowID, nil)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "SOP 不存在"})
		return
	}
	policy := h.repo.EvoSOPPolicy(req.ProjectID, workflowID)
	if !policy.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": "该 SOP 已关闭自动进化，请先开启进化开关"})
		return
	}
	current, loggedIn := h.requestUser(c.Request, true)
	if !loggedIn {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录后再发起优化"})
		return
	}
	user, ok := h.repo.User(current.ID)
	if !ok || strings.TrimSpace(user.CodeBuddyAPIKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先在个人配置中配置自己的 CodeBuddy API Key"})
		return
	}

	runs := h.loadWindowRuns(req.ProjectID, evoDefaultWindowDays, evoSOPWindowLimit)
	sopRuns := filterRunsByWorkflow(runs, workflowID)
	assetSet := h.collectSOPAssetKeys(wf, sopRuns)
	findings := h.sopFindings(h.repo.EvoFindings(req.ProjectID, domain.EvoFindingOpen), workflowID, assetSet)

	var targetAsset evoAsset
	var targetFinding domain.EvoFinding
	for _, f := range findings {
		asset, loaded := h.loadAsset(f.AssetKind, f.AssetID)
		if !loaded || privateEvolutionAsset(asset, req.ProjectID) != nil {
			continue
		}
		if h.repo.EvoLevel(asset.Kind, asset.ID) < domain.EvoLevelPropose {
			continue
		}
		targetAsset = asset
		targetFinding = f
		break
	}
	if targetAsset.ID == "" {
		for _, node := range wf.Nodes {
			if node.AgentID == "" {
				continue
			}
			asset, loaded := h.loadAsset(domain.EvoAssetAgent, node.AgentID)
			if !loaded || privateEvolutionAsset(asset, req.ProjectID) != nil {
				continue
			}
			if h.repo.EvoLevel(asset.Kind, asset.ID) < domain.EvoLevelPropose {
				continue
			}
			if f, found := h.findingForAsset(req.ProjectID, asset); found {
				targetAsset = asset
				targetFinding = f
				break
			}
		}
	}
	if targetAsset.ID == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "当前 SOP 暂无可自动优化的私有角色或待修复问题样本"})
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(policy.Model)
	}
	if model == "" {
		if project, ok := h.repo.Project(req.ProjectID); ok && project.SOPDetection != nil {
			model = strings.TrimSpace(project.SOPDetection.Model)
		}
	}
	catalog := assistantModelsForRequest(c.Request.Context(), user, model)
	p := domain.EvoProposal{
		ID:          "evp-" + randomID(),
		ProjectID:   req.ProjectID,
		FindingID:   targetFinding.ID,
		AssetKind:   targetAsset.Kind,
		AssetID:     targetAsset.ID,
		AssetName:   targetAsset.Name,
		Title:       "正在根据偏好生成 SOP 优化提案…",
		BaseContent: targetAsset.Content,
		BaseHash:    domain.ContentHash(targetAsset.Content),
		Status:      domain.EvoProposalDrafting,
		Model:       assistantAllowedModel(model, catalog),
		CreatedBy:   user.ID,
	}
	p, queued, created, err := h.repo.SubmitEvoProposal(p, "evq-"+randomID(), "")
	if err != nil {
		respondEvoError(c, err)
		return
	}
	if queued != nil {
		c.JSON(http.StatusAccepted, gin.H{"status": queued.Status, "queueId": queued.ID, "queue": queued})
		return
	}
	if created {
		h.launchProposalJob(p, targetFinding, user)
		c.JSON(http.StatusAccepted, evoProposalView(p, false))
		return
	}
	c.JSON(http.StatusOK, evoProposalView(p, false))
}

func (h *evolutionHandlers) buildSOPSummaries(projectID string, days int) []domain.EvoSOPSummary {
	runs := h.loadWindowRuns(projectID, days, evoSOPWindowLimit)
	evals, detections := h.loadSOPRunEvals(runs)
	signals := h.repo.EvoSignals(projectID, time.Now().UTC().AddDate(0, 0, -days))
	findings := h.repo.EvoFindings(projectID, domain.EvoFindingOpen, domain.EvoFindingProposed)
	proposals := h.repo.EvoProposals(projectID, 200)
	policies := h.repo.EvoSOPPolicies(projectID)
	projectModel := ""
	if project, ok := h.repo.Project(projectID); ok && project.SOPDetection != nil {
		projectModel = strings.TrimSpace(project.SOPDetection.Model)
	}

	wfs := h.projectSOPWorkflows(projectID, runs)
	out := make([]domain.EvoSOPSummary, 0, len(wfs))
	for _, wf := range wfs {
		policy, ok := policies[wf.ID]
		if !ok {
			policy = domain.DefaultEvoSOPPolicy(projectID, wf.ID)
		}
		summary, _ := h.computeSOPSummary(projectID, wf, policy, projectModel, runs, evals, detections, signals, findings, proposals)
		out = append(out, summary)
	}
	stageRank := map[string]int{
		domain.EvoSOPStageGray:     0,
		domain.EvoSOPStageOptimize: 1,
		domain.EvoSOPStageMonitor:  2,
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := stageRank[out[i].Stage], stageRank[out[j].Stage]
		if ri != rj {
			return ri < rj
		}
		if out[i].LatestMetrics.Runs != out[j].LatestMetrics.Runs {
			return out[i].LatestMetrics.Runs > out[j].LatestMetrics.Runs
		}
		return out[i].WorkflowName < out[j].WorkflowName
	})
	return out
}

func (h *evolutionHandlers) buildSOPDetail(projectID, workflowID string, days int) (domain.EvoSOPDetail, bool) {
	runs := h.loadWindowRuns(projectID, days, evoSOPWindowLimit)
	wf, ok := h.resolveSOPWorkflow(projectID, workflowID, runs)
	if !ok {
		return domain.EvoSOPDetail{}, false
	}
	evals, detections := h.loadSOPRunEvals(runs)
	signals := h.repo.EvoSignals(projectID, time.Now().UTC().AddDate(0, 0, -days))
	findings := h.repo.EvoFindings(projectID, domain.EvoFindingOpen, domain.EvoFindingProposed)
	proposals := h.repo.EvoProposals(projectID, 200)
	policy := h.repo.EvoSOPPolicy(projectID, workflowID)
	projectModel := ""
	if project, ok := h.repo.Project(projectID); ok && project.SOPDetection != nil {
		projectModel = strings.TrimSpace(project.SOPDetection.Model)
	}

	summary, ctx := h.computeSOPSummary(projectID, wf, policy, projectModel, runs, evals, detections, signals, findings, proposals)
	recentEdits := h.collectSOPExternalEdits(wf, ctx.sopRuns)
	versions := evolution.BuildSOPVersions(wf, ctx.sopRuns, evals, policy.BatchSize)
	versions = evolution.SplitSOPVersionsWithExternalEdits(wf, versions, ctx.sopRuns, evals, recentEdits, policy.BatchSize)
	grayRounds := h.buildSOPGrayRounds(projectID, wf, ctx.sopProposals, evals)

	var activeFull *domain.EvoProposal
	var lastFull *domain.EvoProposal
	if ctx.active != nil {
		if loaded, found := h.repo.EvoProposal(ctx.active.ID); found {
			h.loadHistoricalReplayMeasurements(&loaded)
			v := evoProposalView(loaded, false)
			activeFull = &v
		}
	}
	if ctx.last != nil {
		if activeFull != nil && ctx.last.ID == activeFull.ID {
			lastFull = activeFull
		} else if loaded, found := h.repo.EvoProposal(ctx.last.ID); found {
			h.loadHistoricalReplayMeasurements(&loaded)
			v := evoProposalView(loaded, false)
			lastFull = &v
		}
	}

	changeSource := activeFull
	if changeSource == nil {
		changeSource = lastFull
	}
	changeFiles := evolution.BuildSOPChangeFiles(changeSource)

	batchSlice := ctx.sopRuns
	if len(batchSlice) > policy.BatchSize {
		batchSlice = batchSlice[:policy.BatchSize]
	}
	batchMetrics := evolution.SummarizeSOPRuns(batchSlice, evals)
	problems := evolution.BuildSOPProblems(ctx.sopFindings, ctx.sopSignals, policy.Preferences, changeSource)

	return domain.EvoSOPDetail{
		EvoSOPSummary:  summary,
		Versions:       versions,
		GrayRounds:     grayRounds,
		ActiveProposal: activeFull,
		LastProposal:   lastFull,
		BatchMetrics:   &batchMetrics,
		Problems:       problems,
		ChangeFiles:    changeFiles,
		RecentEdits:    recentEdits,
	}, true
}

func (h *evolutionHandlers) collectSOPExternalEdits(wf domain.WorkflowDefinition, sopRuns []domain.Run) []domain.EvoSOPExternalEdit {
	assetKeys := h.collectSOPAssetKeys(wf, sopRuns)
	var out []domain.EvoSOPExternalEdit
	for key := range assetKeys {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		kind, id := parts[0], parts[1]
		asset, ok := h.loadAsset(kind, id)
		if !ok {
			continue
		}
		vers := h.repo.AssetVersions(kind, id)
		if len(vers) < 2 {
			continue
		}
		// AssetVersions returns newest-first; sort ascending to diff v[i-1] -> v[i].
		asc := append([]domain.AssetVersion(nil), vers...)
		sort.SliceStable(asc, func(i, j int) bool { return asc[i].Version < asc[j].Version })
		for i := 1; i < len(asc); i++ {
			cur := asc[i]
			if cur.Source != domain.EvoVersionChatbot && cur.Source != domain.EvoVersionManual {
				continue
			}
			diff := evolution.BuildAssetDiffFile(kind, id, asset.Name, asc[i-1].Content, cur.Content)
			if diff == nil {
				continue
			}
			out = append(out, domain.EvoSOPExternalEdit{
				ID:        fmt.Sprintf("%s:%s:v%d", kind, id, cur.Version),
				AssetKind: kind,
				AssetID:   id,
				AssetName: asset.Name,
				Version:   cur.Version,
				Source:    cur.Source,
				Note:      cur.Note,
				CreatedAt: cur.CreatedAt,
				Diff:      *diff,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

type sopSummaryContext struct {
	sopRuns      []domain.Run
	sopSignals   []domain.EvoSignal
	sopFindings  []domain.EvoFinding
	sopProposals []domain.EvoProposal
	active       *domain.EvoProposal
	last         *domain.EvoProposal
}

func (h *evolutionHandlers) computeSOPSummary(
	projectID string,
	wf domain.WorkflowDefinition,
	policy domain.EvoSOPPolicy,
	projectModel string,
	allRuns []domain.Run,
	evals map[string]domain.RunEvalRecord,
	detections map[string]domain.RunEvalRecord,
	allSignals []domain.EvoSignal,
	allFindings []domain.EvoFinding,
	allProposals []domain.EvoProposal,
) (domain.EvoSOPSummary, sopSummaryContext) {
	policy.Preferences = evolution.NormalizeSOPPreferences(policy.Preferences)
	policy.BatchSize = evolution.NormalizeSOPBatchSize(policy.BatchSize)

	sopRuns := filterRunsByWorkflow(allRuns, wf.ID)
	runIDSet := map[string]bool{}
	for _, r := range sopRuns {
		runIDSet[r.ID] = true
	}
	var sopSignals []domain.EvoSignal
	for _, s := range allSignals {
		if runIDSet[s.RunID] {
			sopSignals = append(sopSignals, s)
		}
	}

	assetKeys := h.collectSOPAssetKeys(wf, sopRuns)
	sopFindings := h.sopFindings(allFindings, wf.ID, assetKeys)
	findingIDSet := map[string]bool{}
	openFindings := 0
	for _, f := range sopFindings {
		findingIDSet[f.ID] = true
		if f.Status == domain.EvoFindingOpen {
			openFindings++
		}
	}

	var sopProposals []domain.EvoProposal
	for _, p := range allProposals {
		if findingIDSet[p.FindingID] || assetKeys[p.AssetKind+":"+p.AssetID] {
			sopProposals = append(sopProposals, p)
		}
	}

	stage, active, last := evolution.DetermineSOPStage(sopProposals)

	batchSlice := sopRuns
	if len(batchSlice) > policy.BatchSize {
		batchSlice = batchSlice[:policy.BatchSize]
	}
	latestMetrics := evolution.SummarizeSOPRuns(batchSlice, evals)

	var lastRun *domain.Run
	var lastDetection *domain.RunEvalRecord
	if len(sopRuns) > 0 {
		r := sopRuns[0]
		lastRun = &r
		if det, ok := detections[r.ID]; ok {
			d := det
			lastDetection = &d
		}
	}

	var since time.Time
	for _, p := range sopProposals {
		if p.Status == domain.EvoProposalReleased && !p.DecidedAt.IsZero() && p.DecidedAt.After(since) {
			since = p.DecidedAt
		}
	}
	pendingRuns := 0
	for _, r := range sopRuns {
		if since.IsZero() || r.CreatedAt.After(since) {
			pendingRuns++
		}
	}
	if pendingRuns > policy.BatchSize {
		pendingRuns = policy.BatchSize
	}

	model := strings.TrimSpace(policy.Model)
	if model == "" {
		model = projectModel
	}
	activeID := ""
	if active != nil {
		activeID = active.ID
	}

	lastNote := evolution.LastRunSummaryNote(lastRun, sopSignals, lastDetection)
	if latestEdit := h.latestSOPExternalEdit(assetKeys); latestEdit != nil {
		if lastRun == nil || latestEdit.CreatedAt.After(lastRun.CreatedAt) {
			src := "手动修改"
			if latestEdit.Source == domain.EvoVersionChatbot {
				src = "Chatbot 已调整"
			}
			name := latestEdit.AssetName
			if name == "" {
				name = latestEdit.AssetID
			}
			lastNote = fmt.Sprintf("%s「%s」 · 等待新运行验证", src, name)
		}
	}

	summary := domain.EvoSOPSummary{
		WorkflowID:       wf.ID,
		WorkflowName:     wf.Name,
		Description:      wf.Description,
		CurrentVersion:   evolution.FormatSOPVersion(wf.Version),
		Enabled:          policy.Enabled,
		Preferences:      policy.Preferences,
		BatchSize:        policy.BatchSize,
		Model:            model,
		Stage:            stage,
		PendingRuns:      pendingRuns,
		LastRunNote:      lastNote,
		LatestMetrics:    latestMetrics,
		ActiveProposalID: activeID,
		OpenFindings:     openFindings,
	}
	if summary.WorkflowName == "" {
		summary.WorkflowName = wf.ID
	}

	return summary, sopSummaryContext{
		sopRuns:      sopRuns,
		sopSignals:   sopSignals,
		sopFindings:  sopFindings,
		sopProposals: sopProposals,
		active:       active,
		last:         last,
	}
}

func (h *evolutionHandlers) buildSOPGrayRounds(
	projectID string,
	wf domain.WorkflowDefinition,
	proposals []domain.EvoProposal,
	evals map[string]domain.RunEvalRecord,
) []domain.EvoSOPGrayRound {
	var canaryProps []domain.EvoProposal
	for _, p := range proposals {
		if p.Canary != nil {
			canaryProps = append(canaryProps, p)
		}
	}
	if len(canaryProps) == 0 {
		return []domain.EvoSOPGrayRound{}
	}
	sort.SliceStable(canaryProps, func(i, j int) bool {
		return canaryProps[i].Canary.StartedAt.Before(canaryProps[j].Canary.StartedAt)
	})
	if len(canaryProps) > 6 {
		canaryProps = canaryProps[len(canaryProps)-6:]
	}

	rounds := make([]domain.EvoSOPGrayRound, 0, len(canaryProps))
	for _, p := range canaryProps {
		full, ok := h.repo.EvoProposal(p.ID)
		if ok {
			p = full
		}
		if p.Canary == nil {
			continue
		}
		end := p.Canary.EndedAt
		if end.IsZero() && p.Status == domain.EvoProposalReleased && !p.DecidedAt.IsZero() {
			end = p.DecidedAt
		}
		pins := h.repo.AssetPinsBetween(p.AssetKind, p.AssetID, p.Canary.StartedAt, end, evolution.TrialRunScanLimit)
		candHash := domain.ContentHash(p.CandidateContent)

		var ctrlRuns, candRuns []domain.Run
		var ctrlAll, candAll []domain.Run
		seen := map[string]bool{}
		for _, pin := range pins {
			if seen[pin.RunID] {
				continue
			}
			seen[pin.RunID] = true
			run, loaded := h.repo.RunState(pin.RunID)
			if !loaded || !domain.TerminalRunStatus(run.Status) || run.ExecutionContext.IsEvolutionReplay() || h.runProject(run) != projectID {
				continue
			}
			if _, ok := evals[run.ID]; !ok {
				if rec, ok := h.repo.LatestRunEval(run.ID); ok && rec.Status == domain.RunEvalSucceeded && !rec.SOPDetection {
					evals[run.ID] = rec
				}
			}
			switch pin.Hash {
			case p.BaseHash:
				ctrlAll = append(ctrlAll, run)
				if run.WorkflowID == wf.ID {
					ctrlRuns = append(ctrlRuns, run)
				}
			case candHash:
				candAll = append(candAll, run)
				if run.WorkflowID == wf.ID {
					candRuns = append(candRuns, run)
				}
			}
		}
		if len(ctrlRuns) == 0 && len(candRuns) == 0 {
			ctrlRuns, candRuns = ctrlAll, candAll
		}

		ctrlMetrics := evolution.SummarizeSOPRuns(ctrlRuns, evals)
		candMetrics := evolution.SummarizeSOPRuns(candRuns, evals)

		if ctrlMetrics.Runs == 0 && p.Canary.Control.Runs > 0 {
			ctrlMetrics.Runs = p.Canary.Control.Runs
			ctrlMetrics.CleanCount = p.Canary.Control.Clean
			ctrlMetrics.CleanRate = p.Canary.Control.Rate
		}
		if candMetrics.Runs == 0 && p.Canary.Canary.Runs > 0 {
			candMetrics.Runs = p.Canary.Canary.Runs
			candMetrics.CleanCount = p.Canary.Canary.Clean
			candMetrics.CleanRate = p.Canary.Canary.Rate
		}

		type taggedRun struct {
			group string
			run   domain.Run
		}
		var tagged []taggedRun
		for _, r := range ctrlRuns {
			tagged = append(tagged, taggedRun{group: "control", run: r})
		}
		for _, r := range candRuns {
			tagged = append(tagged, taggedRun{group: "candidate", run: r})
		}
		sort.SliceStable(tagged, func(i, j int) bool {
			return tagged[i].run.CreatedAt.After(tagged[j].run.CreatedAt)
		})
		if len(tagged) > 12 {
			tagged = tagged[:12]
		}
		recent := make([]domain.EvoSOPGrayRunEvent, 0, len(tagged))
		for _, tr := range tagged {
			s := evolution.ComputeRunHeuristicScore(tr.run)
			if rec, ok := evals[tr.run.ID]; ok && rec.Status == domain.RunEvalSucceeded && !rec.SOPDetection {
				s = rec.Score
			}
			recent = append(recent, domain.EvoSOPGrayRunEvent{
				RunID:    tr.run.ID,
				Group:    tr.group,
				Tokens:   evolution.RunTokens(tr.run),
				Tools:    evolution.RunToolCalls(tr.run),
				Duration: float64(evolution.RunDurationMs(tr.run)) / 1000.0,
				Clean:    evolution.RunScore(tr.run) == 2,
				Score:    &s,
			})
		}

		baseVer := evolution.FormatSOPVersion(wf.Version)
		if p.BaseVersion > 0 {
			baseVer = evolution.FormatSOPVersion(p.BaseVersion)
		}
		targetVer := evolution.FormatSOPVersion(wf.Version + 1)
		if p.ReleasedVersion > 0 {
			targetVer = evolution.FormatSOPVersion(p.ReleasedVersion)
		} else if p.BaseVersion > 0 {
			targetVer = evolution.FormatSOPVersion(p.BaseVersion + 1)
		}
		status := p.Canary.Status
		published := p.Status == domain.EvoProposalReleased
		if published {
			status = "published"
		}

		rounds = append(rounds, domain.EvoSOPGrayRound{
			ProposalID:  p.ID,
			BaseVersion: baseVer,
			Target:      targetVer,
			Status:      status,
			Published:   published,
			Percent:     p.Canary.Percent,
			MinRuns:     p.Canary.MinRuns,
			Control:     ctrlMetrics,
			Candidate:   candMetrics,
			RecentRuns:  recent,
		})
	}
	return rounds
}

func (h *evolutionHandlers) projectSOPWorkflows(projectID string, runs []domain.Run) []domain.WorkflowDefinition {
	byID := map[string]domain.WorkflowDefinition{}
	order := []string{}
	add := func(wf domain.WorkflowDefinition) {
		if strings.TrimSpace(wf.ID) == "" || wf.Status == "archived" {
			return
		}
		if _, exists := byID[wf.ID]; !exists {
			order = append(order, wf.ID)
		}
		byID[wf.ID] = wf
	}
	for _, wf := range h.repo.Workflows() {
		if wf.ProjectID == projectID {
			add(wf)
		}
	}
	for _, run := range runs {
		if strings.TrimSpace(run.WorkflowID) == "" {
			continue
		}
		if _, exists := byID[run.WorkflowID]; exists {
			continue
		}
		if wf, ok := h.repo.Workflow(run.WorkflowID); ok {
			add(wf)
		} else if run.WorkflowSnapshot.ID != "" {
			add(run.WorkflowSnapshot)
		}
	}
	out := make([]domain.WorkflowDefinition, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

func (h *evolutionHandlers) resolveSOPWorkflow(projectID, workflowID string, runs []domain.Run) (domain.WorkflowDefinition, bool) {
	if wf, ok := h.repo.Workflow(workflowID); ok {
		if wf.ProjectID == "" || wf.ProjectID == projectID || wf.Shared {
			return wf, true
		}
	}
	for _, run := range runs {
		if run.WorkflowID == workflowID && run.WorkflowSnapshot.ID != "" {
			return run.WorkflowSnapshot, true
		}
	}
	return domain.WorkflowDefinition{}, false
}

func (h *evolutionHandlers) loadSOPRunEvals(runs []domain.Run) (map[string]domain.RunEvalRecord, map[string]domain.RunEvalRecord) {
	evals := make(map[string]domain.RunEvalRecord, len(runs))
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
		if rec, ok := h.repo.LatestRunEval(run.ID); ok && rec.Status == domain.RunEvalSucceeded {
			evals[run.ID] = rec
		}
	}
	detections := h.repo.SOPDetectionEvaluations(ids)
	return evals, detections
}

func filterRunsByWorkflow(runs []domain.Run, workflowID string) []domain.Run {
	var out []domain.Run
	for _, r := range runs {
		if r.WorkflowID == workflowID {
			out = append(out, r)
		}
	}
	return out
}

func (h *evolutionHandlers) collectSOPAssetKeys(wf domain.WorkflowDefinition, runs []domain.Run) map[string]bool {
	keys := map[string]bool{}
	addAgent := func(agentID string) {
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			return
		}
		keys[domain.EvoAssetAgent+":"+agentID] = true
		if agent, ok := h.repo.Agent(agentID); ok {
			for _, ruleID := range agent.Rules {
				if strings.TrimSpace(ruleID) != "" {
					keys[domain.EvoAssetRule+":"+strings.TrimSpace(ruleID)] = true
				}
			}
		}
	}
	for _, n := range wf.Nodes {
		addAgent(n.AgentID)
	}
	for _, r := range runs {
		for _, n := range r.WorkflowSnapshot.Nodes {
			addAgent(n.AgentID)
		}
	}
	return keys
}

func (h *evolutionHandlers) sopFindings(all []domain.EvoFinding, workflowID string, assetKeys map[string]bool) []domain.EvoFinding {
	var out []domain.EvoFinding
	for _, f := range all {
		matched := assetKeys[f.AssetKind+":"+f.AssetID]
		if !matched {
			for _, wid := range f.WorkflowIDs {
				if wid == workflowID {
					matched = true
					break
				}
			}
		}
		if matched {
			out = append(out, f)
		}
	}
	return out
}

func (h *evolutionHandlers) latestSOPExternalEdit(assetKeys map[string]bool) *domain.EvoSOPExternalEdit {
	var best *domain.EvoSOPExternalEdit
	for key := range assetKeys {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		latest, ok := h.repo.LatestAssetVersion(parts[0], parts[1])
		if !ok || (latest.Source != domain.EvoVersionChatbot && latest.Source != domain.EvoVersionManual) {
			continue
		}
		if best == nil || latest.CreatedAt.After(best.CreatedAt) {
			name := parts[1]
			if asset, loaded := h.loadAsset(parts[0], parts[1]); loaded && asset.Name != "" {
				name = asset.Name
			}
			best = &domain.EvoSOPExternalEdit{
				AssetKind: parts[0],
				AssetID:   parts[1],
				AssetName: name,
				Version:   latest.Version,
				Source:    latest.Source,
				Note:      latest.Note,
				CreatedAt: latest.CreatedAt,
			}
		}
	}
	return best
}

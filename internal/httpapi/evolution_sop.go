package httpapi

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"

	"github.com/gin-gonic/gin"
)

const evoSOPBatchSweepGap = 30 * time.Second

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
	wf, exists := h.resolveSOPWorkflow(req.ProjectID, workflowID, nil)
	if !exists {
		runs := h.loadWindowRuns(req.ProjectID, evoDefaultWindowDays, evoSOPWindowLimit)
		wf, exists = h.resolveSOPWorkflow(req.ProjectID, workflowID, runs)
	}
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "SOP 不存在或不属于当前项目"})
		return
	}
	cur := h.repo.EvoSOPPolicy(req.ProjectID, wf.ID)
	cur.ProjectID = req.ProjectID
	cur.WorkflowID = wf.ID
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
			if assistantModelUnspecified(model) {
				model = ""
			} else {
				norm, err := normalizeFeatureModel(model)
				if err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if current, loggedIn := h.requestUser(c.Request, true); loggedIn {
					if user, ok := h.repo.User(current.ID); ok {
						resolved, err := resolveFeatureModel(user, norm)
						catalog := assistantModelsForRequest(c.Request.Context(), user, norm)
						inCatalog := func(id string) bool {
							if assistantCatalogLists(catalog, id) {
								return true
							}
							if key, found := h.repo.SharedCodeBuddyKey(); found && strings.TrimSpace(key.APIKey) != "" {
								if m, ok := key.Model(id); ok && m != "" {
									return true
								}
							}
							return false
						}
						if err == nil && resolved != "" {
							if len(catalog.Models) > 0 && !inCatalog(resolved) && !inCatalog(norm) {
								c.JSON(http.StatusBadRequest, gin.H{"error": "所选模型不在当前可用模型列表中，请重新选择"})
								return
							}
							model = resolved
						} else if inCatalog(norm) {
							model = norm
						} else {
							c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
							return
						}
					} else {
						model = norm
					}
				} else {
					model = norm
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
	saved := h.repo.EvoSOPPolicy(req.ProjectID, wf.ID)
	if saved.Enabled {
		go h.checkSOPBatchAutoTrigger(saved)
	}
	c.JSON(http.StatusOK, saved)
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
	current, loggedIn := h.requestUser(c.Request, true)
	if !loggedIn {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录后再发起优化"})
		return
	}
	user, ok := h.repo.User(current.ID)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号不可用"})
		return
	}
	p, queued, created, err := h.startSOPOptimization(c.Request.Context(), req.ProjectID, workflowID, req.Model, user)
	if err != nil {
		respondEvoError(c, err)
		return
	}
	if queued != nil {
		c.JSON(http.StatusAccepted, gin.H{"status": queued.Status, "queueId": queued.ID, "queue": queued})
		return
	}
	if created {
		c.JSON(http.StatusAccepted, evoProposalView(p, false))
		return
	}
	c.JSON(http.StatusOK, evoProposalView(p, false))
}

const evoSOPFailedRetryCooldown = 10 * time.Minute

func (h *evolutionHandlers) maybeSweepSOPBatches(now time.Time) {
	h.sopBatchM.Lock()
	due := now.Sub(h.sopBatchAt) >= evoSOPBatchSweepGap
	if due {
		h.sopBatchAt = now
	}
	h.sopBatchM.Unlock()
	if !due {
		return
	}
	h.sweepSOPBatches(now)
}

func (h *evolutionHandlers) sweepSOPBatches(now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("evolution sop batch sweep panic: %v", r)
		}
	}()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// Only sweep explicitly saved & enabled SOP policies so unconfigured SOPs never burn tokens unexpectedly.
	runsByProject := map[string][]domain.Run{}
	for _, policy := range h.repo.AllEnabledEvoSOPPolicies() {
		if !policy.Enabled || strings.TrimSpace(policy.ProjectID) == "" || strings.TrimSpace(policy.WorkflowID) == "" {
			continue
		}
		runs, loaded := runsByProject[policy.ProjectID]
		if !loaded {
			runs = h.loadWindowRuns(policy.ProjectID, evoDefaultWindowDays, evoSOPWindowLimit)
			runsByProject[policy.ProjectID] = runs
		}
		h.checkSOPBatchAutoTriggerWithRuns(policy, runs, now)
	}
}

func (h *evolutionHandlers) checkSOPBatchAutoTrigger(policy domain.EvoSOPPolicy) {
	if !policy.Enabled || strings.TrimSpace(policy.ProjectID) == "" || strings.TrimSpace(policy.WorkflowID) == "" {
		return
	}
	runs := h.loadWindowRuns(policy.ProjectID, evoDefaultWindowDays, evoSOPWindowLimit)
	h.checkSOPBatchAutoTriggerWithRuns(policy, runs, time.Now().UTC())
}

func (h *evolutionHandlers) checkSOPBatchAutoTriggerWithRuns(policy domain.EvoSOPPolicy, runs []domain.Run, now time.Time) {
	if !policy.Enabled || strings.TrimSpace(policy.ProjectID) == "" || strings.TrimSpace(policy.WorkflowID) == "" {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	policy.BatchSize = evolution.NormalizeSOPBatchSize(policy.BatchSize)
	wf, ok := h.resolveSOPWorkflow(policy.ProjectID, policy.WorkflowID, runs)
	if !ok {
		return
	}
	sopRuns := filterRunsByWorkflow(runs, wf.ID)
	if len(sopRuns) < policy.BatchSize {
		return
	}
	assetKeys := h.collectSOPAssetKeys(wf, sopRuns)
	allFindings := h.repo.EvoFindings(policy.ProjectID, domain.EvoFindingOpen, domain.EvoFindingProposed)
	findings := h.sopFindings(allFindings, wf.ID, assetKeys)
	proposals := h.repo.EvoProposals(policy.ProjectID, 200)
	sopProposals := h.filterSOPProposals(wf.ID, assetKeys, allFindings, findings, proposals, len(sopRuns) > 0)
	stage, _, last := evolution.DetermineSOPStage(sopProposals)
	if stage != domain.EvoSOPStageMonitor {
		return
	}
	pending := countSOPPendingRuns(sopRuns, sopProposals, policy.BatchSize)
	if pending < policy.BatchSize {
		log.Printf("evolution sop auto-batch %s/%s waiting: %d/%d pending runs (total window runs=%d)", policy.ProjectID, wf.ID, pending, policy.BatchSize, len(sopRuns))
		return
	}
	// If the most recent proposal failed without any newer run since then, respect
	// a brief cooldown to avoid tight failure loops while still recovering automatically.
	if last != nil && last.Status == domain.EvoProposalFailed && len(sopRuns) > 0 && !sopRuns[0].CreatedAt.After(last.UpdatedAt) {
		if now.Sub(last.UpdatedAt) < evoSOPFailedRetryCooldown {
			log.Printf("evolution sop auto-batch %s/%s in cooldown after failed proposal %s (%s)", policy.ProjectID, wf.ID, last.ID, last.Error)
			return
		}
	}
	user, resolvedModel, ok := h.resolveSOPActorUser(policy, wf)
	if !ok {
		log.Printf("evolution sop auto-batch %s/%s skipped (%d/%d runs): policy owner %d lacks project write permission or CLI credentials", policy.ProjectID, wf.ID, pending, policy.BatchSize, policy.UpdatedBy)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, _, created, err := h.startSOPOptimization(ctx, policy.ProjectID, wf.ID, resolvedModel, user); err != nil {
		log.Printf("evolution sop auto-batch %s/%s: %v", policy.ProjectID, wf.ID, err)
	} else if created {
		log.Printf("evolution sop auto-batch triggered optimization for %s/%s (%d/%d runs)", policy.ProjectID, wf.ID, pending, policy.BatchSize)
	}
}

func (h *evolutionHandlers) workflowPreferredModel(wf domain.WorkflowDefinition) string {
	for _, node := range wf.Nodes {
		if strings.TrimSpace(node.AgentID) == "" {
			continue
		}
		agent, ok := h.repo.Agent(node.AgentID)
		if !ok {
			continue
		}
		m := strings.TrimSpace(agent.Model)
		if m == "" || assistantModelUnspecified(m) {
			continue
		}
		if strings.Contains(m, ":") {
			return m
		}
		execID := strings.TrimSpace(agent.ExecutorID)
		if execID != "" && execID != assistantEngineCodeBuddy {
			return execID + ":" + m
		}
		return m
	}
	return ""
}

func (h *evolutionHandlers) applySOPModelCredentials(ctx context.Context, user domain.User, requestedModel string, wf domain.WorkflowDefinition) (domain.User, string, bool) {
	model := strings.TrimSpace(requestedModel)
	if model == "" {
		model = h.workflowPreferredModel(wf)
	}
	if evoImproveOverride != nil {
		return user, model, true
	}
	reqModel := model
	if reqModel == "" {
		reqModel = assistantDefaultModel
	}
	if m, err := resolveFeatureModel(user, reqModel); err == nil && m != "" {
		return user, m, true
	}
	if strings.TrimSpace(user.CodeBuddyAPIKey) != "" {
		catalog := assistantModelsForRequest(ctx, user, model)
		return user, assistantAllowedModel(model, catalog), true
	}
	// Fall back to platform shared CodeBuddy key when the policy owner has no personal CLI credentials.
	if key, found := h.repo.SharedCodeBuddyKey(); found && strings.TrimSpace(key.APIKey) != "" && len(key.Models) > 0 {
		if key.SponsorID == 0 || key.SponsorID != user.ID {
			sharedUser := user
			sharedUser.CodeBuddyAPIKey = key.APIKey
			sharedUser.CodeBuddyBaseURL = ""
			sharedUser.CodeBuddyInternetEnvironment = key.InternetEnvironment
			sharedUser.CodeBuddyModels = nil
			chosen := ""
			if m, ok := key.Model(model); ok && m != "" {
				chosen = m
			} else if len(key.Models) > 0 {
				chosen = key.Models[0]
			}
			return sharedUser, chosen, true
		}
	}
	return user, "", false
}

// resolveSOPActorUser strictly authorizes automated SOP jobs using ONLY the
// user who explicitly enabled the SOP policy (policy.UpdatedBy) and confirms
// they still hold project write permission. It never borrows keys from viewers,
// other members, or platform administrators.
func (h *evolutionHandlers) resolveSOPActorUser(policy domain.EvoSOPPolicy, wf domain.WorkflowDefinition) (domain.User, string, bool) {
	if policy.UpdatedBy <= 0 {
		return domain.User{}, "", false
	}
	user, ok := h.repo.User(policy.UpdatedBy)
	if !ok {
		return domain.User{}, "", false
	}
	isAdmin := strings.TrimSpace(user.LoginName) != "" && h.repo.IsPlatformAdmin(strings.TrimSpace(user.LoginName))
	if !isAdmin && !h.projectMemberWrites(policy.ProjectID, user.ID) {
		return domain.User{}, "", false
	}
	reqModel := strings.TrimSpace(policy.Model)
	if reqModel == "" {
		if proj, ok := h.repo.Project(policy.ProjectID); ok && proj.SOPDetection != nil {
			reqModel = strings.TrimSpace(proj.SOPDetection.Model)
		}
	}
	return h.applySOPModelCredentials(context.Background(), user, reqModel, wf)
}

func (h *evolutionHandlers) startSOPOptimization(ctx context.Context, projectID, workflowID, requestedModel string, user domain.User) (domain.EvoProposal, *domain.EvoQueueItem, bool, error) {
	runs := h.loadWindowRuns(projectID, evoDefaultWindowDays, evoSOPWindowLimit)
	wf, ok := h.resolveSOPWorkflow(projectID, workflowID, runs)
	if !ok {
		return domain.EvoProposal{}, nil, false, &evoError{status: http.StatusNotFound, msg: "SOP 不存在"}
	}
	policy := h.repo.EvoSOPPolicy(projectID, workflowID)
	if !policy.Enabled {
		return domain.EvoProposal{}, nil, false, evoConflict("该 SOP 已关闭自动进化，请先开启进化开关")
	}
	policy.Preferences = evolution.NormalizeSOPPreferences(policy.Preferences)
	policy.BatchSize = evolution.NormalizeSOPBatchSize(policy.BatchSize)

	model := strings.TrimSpace(requestedModel)
	if model == "" {
		model = strings.TrimSpace(policy.Model)
	}
	if model == "" {
		if project, ok := h.repo.Project(projectID); ok && project.SOPDetection != nil {
			model = strings.TrimSpace(project.SOPDetection.Model)
		}
	}
	resolvedUser, resolvedModel, credOK := h.applySOPModelCredentials(ctx, user, model, wf)
	if !credOK {
		return domain.EvoProposal{}, nil, false, &evoError{status: http.StatusBadRequest, msg: "请先在个人配置中配置可用的 AI 模型凭据（CodeBuddy / Codex / Claude 等）"}
	}
	user = resolvedUser

	sopRuns := filterRunsByWorkflow(runs, workflowID)
	batchRuns := sopRuns
	if len(batchRuns) > policy.BatchSize {
		batchRuns = batchRuns[:policy.BatchSize]
	}
	evals, _ := h.loadSOPRunEvals(batchRuns)
	batchMetrics := evolution.SummarizeSOPRuns(batchRuns, evals)
	assetSet := h.collectSOPAssetKeys(wf, sopRuns)
	findings := h.sopFindings(h.repo.EvoFindings(projectID, domain.EvoFindingOpen), workflowID, assetSet)

	var targetAsset evoAsset
	var targetNodeID string
	var targetFinding domain.EvoFinding

	for _, f := range findings {
		asset, loaded := h.loadAsset(f.AssetKind, f.AssetID)
		if !loaded || privateEvolutionAsset(asset, projectID) != nil {
			continue
		}
		if h.repo.EvoLevel(asset.Kind, asset.ID) < domain.EvoLevelPropose {
			continue
		}
		targetAsset = asset
		targetFinding = f
		for _, node := range wf.Nodes {
			if asset.Kind == domain.EvoAssetAgent && node.AgentID == asset.ID {
				targetNodeID = node.ID
				break
			}
		}
		break
	}

	if targetAsset.ID == "" {
		// Pick the highest-impact private evolvable agent on this SOP following
		// the user's P1 > P2 > ... preference priority order.
		type cand struct {
			asset      evoAsset
			nodeID     string
			tokens     int64
			tools      int
			durationMs int64
			unclean    int
		}
		betterCand := func(a, b cand) bool {
			for _, pref := range policy.Preferences {
				switch pref {
				case domain.EvoPrefTokens:
					if a.tokens != b.tokens {
						return a.tokens > b.tokens
					}
				case domain.EvoPrefTools:
					if a.tools != b.tools {
						return a.tools > b.tools
					}
				case domain.EvoPrefDuration:
					if a.durationMs != b.durationMs {
						return a.durationMs > b.durationMs
					}
				case domain.EvoPrefClean:
					if a.unclean != b.unclean {
						return a.unclean > b.unclean
					}
				}
			}
			if a.tokens != b.tokens {
				return a.tokens > b.tokens
			}
			return a.tools > b.tools
		}
		var best *cand
		for _, node := range wf.Nodes {
			if node.AgentID == "" {
				continue
			}
			asset, loaded := h.loadAsset(domain.EvoAssetAgent, node.AgentID)
			if !loaded || privateEvolutionAsset(asset, projectID) != nil {
				continue
			}
			if h.repo.EvoLevel(asset.Kind, asset.ID) < domain.EvoLevelPropose {
				continue
			}
			cur := cand{asset: asset, nodeID: node.ID}
			for _, r := range batchRuns {
				if nr := r.Nodes[node.ID]; nr != nil {
					cur.tokens += evolution.NodeTokens(*nr)
					cur.tools += nr.ToolCalls
					if !nr.StartedAt.IsZero() && !nr.FinishedAt.IsZero() && nr.FinishedAt.After(nr.StartedAt) {
						cur.durationMs += nr.FinishedAt.Sub(nr.StartedAt).Milliseconds()
					}
					if nr.Status != "succeeded" || nr.ReworkCount > 0 || nr.Attempt > 1 {
						cur.unclean += 1 + nr.ReworkCount + max(0, nr.Attempt-1)
					}
				}
			}
			if best == nil || betterCand(cur, *best) {
				copyCur := cur
				best = &copyCur
			}
		}
		if best != nil {
			targetAsset = best.asset
			targetNodeID = best.nodeID
			if f, found := h.findingForAsset(projectID, targetAsset); found {
				targetFinding = f
			}
		}
	}

	if targetAsset.ID == "" {
		return domain.EvoProposal{}, nil, false, evoConflict("当前 SOP 暂无可自动优化的私有角色（共享/平台角色请先复制为项目私有角色）")
	}

	// Build or enrich finding with the user's prioritized SOP optimization preferences & batch runs
	// so Token/Tools/Duration/Clean optimization strictly follows P1 > P2 > ... order.
	if targetFinding.ID == "" {
		if len(batchRuns) == 0 {
			return domain.EvoProposal{}, nil, false, evoConflict("当前 SOP 尚无已完成的业务运行样本，至少完成 1 次运行后再优化")
		}
		f := evolution.BuildSOPPreferenceFinding(projectID, wf, targetAsset.Kind, targetAsset.ID, targetAsset.Name, targetNodeID, policy, batchRuns, batchMetrics)
		f.ID = "evf-" + randomID()
		if err := h.repo.PutEvoFinding(f); err != nil {
			return domain.EvoProposal{}, nil, false, err
		}
		targetFinding = f
	} else if len(batchRuns) > 0 {
		prefFinding := evolution.BuildSOPPreferenceFinding(projectID, wf, targetAsset.Kind, targetAsset.ID, targetAsset.Name, targetNodeID, policy, batchRuns, batchMetrics)
		priorityText := evolution.SOPPreferencePriorityText(policy.Preferences)
		guidance := evolution.SOPPreferencePromptGuidance(policy.Preferences)
		prefNote := fmt.Sprintf("【SOP 优化优先级：%s】近 %d 次运行平均 Token %d、平均工具调用 %.1f 次、平均耗时 %.1fs、一次成功率 %.0f%%。%s",
			priorityText, batchMetrics.Runs, batchMetrics.AvgTokens, batchMetrics.AvgTools, batchMetrics.AvgDuration, batchMetrics.CleanRate*100, guidance)
		if !strings.Contains(targetFinding.Summary, "SOP 优化优先级") && !strings.Contains(targetFinding.Summary, "SOP 优化偏好") {
			if targetFinding.Summary != "" {
				targetFinding.Summary = prefNote + "\n" + targetFinding.Summary
			} else {
				targetFinding.Summary = prefNote
			}
		}
		seenSample := map[string]bool{}
		for _, id := range targetFinding.SampleRunIDs {
			seenSample[id] = true
		}
		for _, id := range prefFinding.SampleRunIDs {
			if !seenSample[id] && len(targetFinding.SampleRunIDs) < 8 {
				seenSample[id] = true
				targetFinding.SampleRunIDs = append(targetFinding.SampleRunIDs, id)
			}
		}
		seenPrefSig := map[string]bool{}
		for _, s := range targetFinding.Signals {
			if s.Kind == evolution.SignalSOPPreference {
				seenPrefSig[s.RunID] = true
			}
		}
		for _, s := range prefFinding.Signals {
			if !seenPrefSig[s.RunID] {
				seenPrefSig[s.RunID] = true
				targetFinding.Signals = append(targetFinding.Signals, s)
			}
		}
		_ = h.repo.PutEvoFinding(targetFinding)
	}

	p := domain.EvoProposal{
		ID:          "evp-" + randomID(),
		ProjectID:   projectID,
		FindingID:   targetFinding.ID,
		AssetKind:   targetAsset.Kind,
		AssetID:     targetAsset.ID,
		AssetName:   targetAsset.Name,
		Title:       "正在根据偏好生成 SOP 优化提案…",
		BaseContent: targetAsset.Content,
		BaseHash:    domain.ContentHash(targetAsset.Content),
		Status:      domain.EvoProposalDrafting,
		Model:       resolvedModel,
		CreatedBy:   user.ID,
	}
	p, queued, created, err := h.repo.SubmitEvoProposal(p, "evq-"+randomID(), "")
	if err != nil {
		return domain.EvoProposal{}, nil, false, err
	}
	if queued != nil {
		return p, queued, false, nil
	}
	if created {
		h.launchProposalJob(p, targetFinding, user)
		return p, nil, true, nil
	}
	return p, nil, false, nil
}

func countSOPPendingRuns(sopRuns []domain.Run, sopProposals []domain.EvoProposal, batchSize int) int {
	var since time.Time
	for _, p := range sopProposals {
		// A failed proposal must not discard accumulated runs from the current batch.
		if p.Status == domain.EvoProposalFailed {
			continue
		}
		at := p.CreatedAt
		if !p.DecidedAt.IsZero() && p.DecidedAt.After(at) {
			at = p.DecidedAt
		}
		if at.After(since) {
			since = at
		}
	}
	pending := 0
	for _, r := range sopRuns {
		if since.IsZero() || r.CreatedAt.After(since) {
			pending++
		}
	}
	if pending > batchSize {
		pending = batchSize
	}
	return pending
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
	assets := h.collectSOPAssets(projectID, wf, ctx.sopRuns)
	versions := evolution.BuildSOPVersions(wf, ctx.sopRuns, evals, policy.BatchSize)
	if len(assets) > 0 && assets[0].VersionCount >= 2 {
		primary := assets[0]
		assetVers := h.repo.AssetVersions(primary.Kind, primary.ID)
		runPinVer := map[string]int{}
		for _, r := range ctx.sopRuns {
			for _, pin := range h.repo.RunAssetPins(r.ID) {
				if pin.AssetKind == primary.Kind && pin.AssetID == primary.ID {
					if v := resolvePinAssetVersion(pin, r.CreatedAt, assetVers); v > 0 {
						runPinVer[r.ID] = v
					}
					break
				}
			}
		}
		versions = evolution.SplitSOPVersionsByAssetHistory(wf, versions, ctx.sopRuns, evals, primary, assetVers, runPinVer, policy.BatchSize)
	} else {
		versions = evolution.SplitSOPVersionsWithExternalEdits(wf, versions, ctx.sopRuns, evals, recentEdits, policy.BatchSize)
	}
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
		Assets:         assets,
	}, true
}

func (h *evolutionHandlers) collectSOPAssets(projectID string, wf domain.WorkflowDefinition, sopRuns []domain.Run) []domain.EvoSOPAssetRef {
	seen := map[string]bool{}
	var out []domain.EvoSOPAssetRef
	add := func(kind, id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		key := kind + ":" + id
		if seen[key] {
			return
		}
		seen[key] = true
		asset, ok := h.loadAsset(kind, id)
		if !ok {
			return
		}
		vers := h.repo.AssetVersions(kind, id)
		liveHash := domain.ContentHash(asset.Content)
		curVer := 0
		for _, v := range vers {
			if v.Hash == liveHash {
				curVer = v.Version
				break
			}
		}
		if curVer == 0 && len(vers) > 0 {
			curVer = vers[0].Version
		}
		name := strings.TrimSpace(asset.Name)
		if name == "" {
			name = asset.ID
		}
		out = append(out, domain.EvoSOPAssetRef{
			Kind:           kind,
			ID:             id,
			Name:           name,
			CurrentVersion: curVer,
			VersionCount:   len(vers),
			ReadOnly:       privateEvolutionAsset(asset, projectID) != nil,
		})
	}
	for _, n := range wf.Nodes {
		if strings.TrimSpace(n.AgentID) == "" {
			continue
		}
		add(domain.EvoAssetAgent, n.AgentID)
		if agent, ok := h.repo.Agent(n.AgentID); ok {
			for _, ruleID := range agent.Rules {
				add(domain.EvoAssetRule, ruleID)
			}
		}
	}
	for _, r := range sopRuns {
		for _, n := range r.WorkflowSnapshot.Nodes {
			if strings.TrimSpace(n.AgentID) == "" {
				continue
			}
			add(domain.EvoAssetAgent, n.AgentID)
		}
	}
	// Put editable project-private assets before read-only shared assets.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ReadOnly != out[j].ReadOnly {
			return !out[i].ReadOnly
		}
		return out[i].VersionCount > out[j].VersionCount
	})
	return out
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
			if cur.Source != domain.EvoVersionChatbot && cur.Source != domain.EvoVersionManual && cur.Source != domain.EvoVersionRollback {
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
	openFindings := 0
	for _, f := range sopFindings {
		if f.Status == domain.EvoFindingOpen {
			openFindings++
		}
	}

	sopProposals := h.filterSOPProposals(wf.ID, assetKeys, allFindings, sopFindings, allProposals, len(sopRuns) > 0)

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

	pendingRuns := countSOPPendingRuns(sopRuns, sopProposals, policy.BatchSize)

	model := strings.TrimSpace(policy.Model)
	if model == "" {
		model = projectModel
	}
	activeID := ""
	if active != nil {
		activeID = active.ID
	}

	lastNote := evolution.LastRunSummaryNote(lastRun, sopSignals, lastDetection)
	if stage == domain.EvoSOPStageMonitor && last != nil && last.Status == domain.EvoProposalFailed {
		if lastRun == nil || !lastRun.CreatedAt.After(last.UpdatedAt) {
			reason := strings.TrimSpace(last.Error)
			if reason == "" {
				reason = "优化生成或回放未通过"
			}
			lastNote = "上次优化失败 · " + domain.TrimEvoText(reason, 56)
		}
	}
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
		seen := map[string]bool{}
		for _, pin := range pins {
			if seen[pin.RunID] {
				continue
			}
			seen[pin.RunID] = true
			run, loaded := h.repo.RunState(pin.RunID)
			if !loaded || !domain.TerminalRunStatus(run.Status) || run.ExecutionContext.IsEvolutionReplay() || h.runProject(run) != projectID || run.WorkflowID != wf.ID {
				continue
			}
			if _, ok := evals[run.ID]; !ok {
				if rec, ok := h.repo.LatestRunEval(run.ID); ok && rec.Status == domain.RunEvalSucceeded && !rec.SOPDetection {
					evals[run.ID] = rec
				}
			}
			switch pin.Hash {
			case p.BaseHash:
				ctrlRuns = append(ctrlRuns, run)
			case candHash:
				candRuns = append(candRuns, run)
			}
		}

		ctrlMetrics := evolution.SummarizeSOPRuns(ctrlRuns, evals)
		candMetrics := evolution.SummarizeSOPRuns(candRuns, evals)
		if ctrlMetrics.Runs == 0 && candMetrics.Runs == 0 && p.Status != domain.EvoProposalCanary {
			continue
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
		if len(f.WorkflowIDs) > 0 {
			matched := false
			for _, wid := range f.WorkflowIDs {
				if wid == workflowID {
					matched = true
					break
				}
			}
			if matched {
				out = append(out, f)
			}
			continue
		}
		if assetKeys[f.AssetKind+":"+f.AssetID] {
			out = append(out, f)
		}
	}
	return out
}

func (h *evolutionHandlers) filterSOPProposals(
	workflowID string,
	assetKeys map[string]bool,
	allFindings []domain.EvoFinding,
	sopFindings []domain.EvoFinding,
	allProposals []domain.EvoProposal,
	hasSOPRuns bool,
) []domain.EvoProposal {
	findingIDSet := map[string]bool{}
	for _, f := range sopFindings {
		findingIDSet[f.ID] = true
	}
	findingByID := map[string]domain.EvoFinding{}
	for _, f := range allFindings {
		findingByID[f.ID] = f
	}
	var out []domain.EvoProposal
	for _, p := range allProposals {
		if p.FindingID != "" {
			if findingIDSet[p.FindingID] {
				out = append(out, p)
				continue
			}
			if f, ok := findingByID[p.FindingID]; ok {
				if len(f.WorkflowIDs) > 0 {
					continue
				}
			} else if f, ok := h.repo.EvoFinding(p.FindingID); ok && len(f.WorkflowIDs) > 0 {
				matched := false
				for _, wid := range f.WorkflowIDs {
					if wid == workflowID {
						matched = true
						break
					}
				}
				if matched {
					out = append(out, p)
				}
				continue
			}
		}
		if hasSOPRuns && assetKeys[p.AssetKind+":"+p.AssetID] {
			out = append(out, p)
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

// resolvePinAssetVersion maps a run's asset pin to its exact asset version.
// When the pin only carries a hash (pin.Version == 0), it picks the latest
// version with that hash whose CreatedAt is not after the run's CreatedAt
// (falling back to the earliest version with that hash), so historical runs
// are never misattributed to a later rollback version that shares the same hash.
func resolvePinAssetVersion(pin domain.RunAssetPin, runAt time.Time, assetVersDesc []domain.AssetVersion) int {
	if pin.Version > 0 {
		return pin.Version
	}
	if strings.TrimSpace(pin.Hash) == "" || len(assetVersDesc) == 0 {
		return 0
	}
	asc := append([]domain.AssetVersion(nil), assetVersDesc...)
	sort.SliceStable(asc, func(i, j int) bool { return asc[i].Version < asc[j].Version })
	earliest := 0
	matched := 0
	for _, av := range asc {
		if av.Hash != pin.Hash {
			continue
		}
		if earliest == 0 {
			earliest = av.Version
		}
		if runAt.IsZero() || av.CreatedAt.IsZero() || !av.CreatedAt.After(runAt) {
			matched = av.Version
		}
	}
	if matched > 0 {
		return matched
	}
	return earliest
}

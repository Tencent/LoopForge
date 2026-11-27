package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

const (
	evoProposalJobTimeout   = 15 * time.Minute
	evoReplaySweepInterval  = 20 * time.Second
	evoDefaultReplayTimeout = 2 * time.Hour
	evoDefaultWaitGrace     = 30 * time.Minute
	evoReplayStartGrace     = 5 * time.Minute
	evoSampleWindowDays     = 30
)

// evoError carries an HTTP status for flow errors (conflicts, bad state).
type evoError struct {
	status int
	msg    string
}

func (e *evoError) Error() string { return e.msg }

func evoConflict(msg string) error { return &evoError{status: http.StatusConflict, msg: msg} }

func respondEvoError(c *gin.Context, err error) {
	if errors.Is(err, repo.ErrEvoStale) || errors.Is(err, repo.ErrEvoBusy) || errors.Is(err, repo.ErrEvoParallel) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	var flow *evoError
	if errors.As(err, &flow) {
		c.JSON(flow.status, gin.H{"error": flow.msg})
		return
	}
	respond(c, err)
}

// evoImproveFunc calls the improver model. Tests replace it.
type evoImproveFunc func(ctx context.Context, p domain.EvoProposal, system, prompt string, user domain.User) (string, error)

var (
	evoImproveMu       sync.Mutex
	evoImproveOverride evoImproveFunc
)

func setEvoImproveForTest(fn evoImproveFunc) {
	evoImproveMu.Lock()
	evoImproveOverride = fn
	evoImproveMu.Unlock()
}

func currentEvoImprove() evoImproveFunc {
	evoImproveMu.Lock()
	defer evoImproveMu.Unlock()
	if evoImproveOverride != nil {
		return evoImproveOverride
	}
	return func(ctx context.Context, p domain.EvoProposal, system, prompt string, user domain.User) (string, error) {
		raw, _, err := runAssistantCLI(ctx, assistantCLIRequest{Prompt: prompt, Model: p.Model, SystemPrompt: system, UserID: user.ID, ReviewOnly: true}, user, func(assistantStreamEvent) {})
		return raw, err
	}
}

func evoDurationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// findingForAsset reuses the asset's open finding, or diagnoses the window
// for this asset alone and stores the result.
func (h *evolutionHandlers) findingForAsset(projectID string, asset evoAsset) (domain.EvoFinding, bool) {
	if f, ok := h.repo.ActiveEvoFindingForAsset(projectID, asset.Kind, asset.ID); ok {
		return f, true
	}
	days := evoDefaultWindowDays
	runs := h.loadWindowRuns(projectID, days, evoMaxDiagnoseRuns)
	found := evolution.Diagnose(h.diagnoseInput(projectID, days, runs, func(kind, id string) int {
		if kind == asset.Kind && id == asset.ID {
			return domain.EvoLevelPropose
		}
		return h.repo.EvoLevel(kind, id)
	}))
	for _, f := range found {
		if f.AssetKind == asset.Kind && f.AssetID == asset.ID {
			f.ID = "evf-" + randomID()
			if err := h.repo.PutEvoFinding(f); err != nil {
				return domain.EvoFinding{}, false
			}
			return f, true
		}
	}
	return domain.EvoFinding{}, false
}

func (h *evolutionHandlers) launchProposalJob(p domain.EvoProposal, finding domain.EvoFinding, user domain.User) {
	go h.runProposalJob(p, finding, user)
}

func (h *evolutionHandlers) failProposal(p domain.EvoProposal, msg string) {
	current, ok := h.repo.EvoProposal(p.ID)
	if !ok || (current.Status != domain.EvoProposalDrafting && current.Status != domain.EvoProposalValidating) {
		return
	}
	p = current
	p.Status = domain.EvoProposalFailed
	p.Error = msg
	if strings.HasPrefix(p.Title, "正在生成") {
		p.Title = "提案生成失败"
	}
	if err := h.repo.PutEvoProposal(p); err != nil {
		log.Printf("evolution proposal %s fail persist: %v", p.ID, err)
		return
	}
	h.recordSOPProposalOutcome(p, false, msg, time.Now().UTC())
}

func (h *evolutionHandlers) runProposalJob(p domain.EvoProposal, finding domain.EvoFinding, user domain.User) {
	ctx, cancel := context.WithTimeout(context.Background(), evoProposalJobTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("evolution proposal %s panic: %v", p.ID, r)
			h.failProposal(p, fmt.Sprintf("提案任务异常：%v", r))
		}
	}()
	select {
	case h.jobs <- struct{}{}:
		defer func() { <-h.jobs }()
	case <-ctx.Done():
		h.failProposal(p, "提案排队超时")
		return
	}
	current, exists := h.repo.EvoProposal(p.ID)
	if !exists || current.Status != domain.EvoProposalDrafting {
		return
	}
	p = current
	asset, ok := h.loadAsset(p.AssetKind, p.AssetID)
	if !ok {
		h.failProposal(p, "资产已删除")
		return
	}
	plans := h.planReplays(p, finding, asset)
	for _, plan := range plans {
		if err := h.engine.ValidateEvolutionReplay(plan.Run); err != nil {
			h.failProposal(p, "样本 "+plan.Run.ID+" 不可回放："+err.Error())
			return
		}
	}
	evidence := []evolution.Evidence{}
	for _, plan := range plans {
		if plan.Group == domain.EvoGroupDiagnosis {
			tools, _ := h.repo.SOPDetectionTools(plan.Run.ID, 120)
			evidence = append(evidence, evolution.BuildEvidenceWithTools(plan.Run, finding, tools))
		}
	}
	if len(evidence) == 0 {
		h.failProposal(p, "没有可回放的样本（样本运行可能已删除、仍在运行，或不属于可回放的 SOP）")
		return
	}
	system, prompt := evolution.BuildImproverPrompt(asset.Kind, asset.Name, asset.Content, finding, evidence, h.experienceLines(p))
	raw, err := currentEvoImprove()(ctx, p, system, prompt, user)
	if err != nil {
		h.failProposal(p, "改进器调用失败："+err.Error())
		return
	}
	candidate, err := evolution.ParseCandidate(raw)
	if err != nil {
		h.failProposal(p, err.Error())
		return
	}
	if err := evolution.StaticCheck(asset.Content, candidate.Content); err != nil {
		h.failProposal(p, "静态检查未通过："+err.Error())
		return
	}
	if latest, ok := h.loadAsset(p.AssetKind, p.AssetID); !ok || domain.ContentHash(latest.Content) != p.BaseHash {
		h.failProposal(p, "生成期间资产被修改过，请重新生成")
		return
	}
	if current, ok := h.repo.EvoProposal(p.ID); ok {
		if current.Status != domain.EvoProposalDrafting {
			return
		}
		p = current
	}
	p.Title, p.Rationale, p.CandidateContent = candidate.Title, candidate.Rationale, candidate.Content
	p.Review = evolution.CandidateReview(candidate, p.BaseContent, evidence)
	p.ExpectedReplays = len(plans)
	p.Status = domain.EvoProposalValidating
	if err := h.repo.PutEvoProposal(p); err != nil {
		h.failProposal(p, err.Error())
		return
	}
	overrides := domain.EvoAssetOverride{}
	if asset.Kind == domain.EvoAssetAgent {
		overrides.AgentPrompts = map[string]string{asset.ID: candidate.Content}
	} else {
		overrides.RuleContents = map[string]string{asset.ID: candidate.Content}
	}
	for _, plan := range plans {
		h.startReplay(p, plan, asset, overrides, user)
	}
	h.sweepProposal(p, time.Now().UTC())
}

// planReplays picks diagnosis / holdout failures from the finding, pinned
// samples and recent clean successes that used the asset.
func (h *evolutionHandlers) planReplays(p domain.EvoProposal, finding domain.EvoFinding, asset evoAsset) []evolution.SamplePlan {
	failing := []domain.Run{}
	for _, id := range finding.SampleRunIDs {
		if len(failing) >= evolution.MaxDiagnosisSamples+evolution.MaxHoldoutSamples+2 {
			break
		}
		run, ok := h.repo.RunState(id)
		if !ok || h.runProject(run) != p.ProjectID || run.ExecutionContext.IsEvolutionReplay() {
			continue
		}
		if score := evolution.RunScore(run); score == 0 || score == 1 || evolution.FeedbackSample(run, finding) {
			failing = append(failing, run)
		}
	}
	pinned := []domain.Run{}
	for _, sample := range h.repo.EvoSamples(p.ProjectID) {
		run, ok := h.repo.RunState(sample.RunID)
		if ok && h.runProject(run) == p.ProjectID && h.runUsesAsset(run, asset) && evolution.RunScore(run) == 2 {
			pinned = append(pinned, run)
		}
	}
	succeeded := []domain.Run{}
	seen := map[string]bool{}
	consider := func(run domain.Run) {
		if seen[run.ID] || h.runProject(run) != p.ProjectID || run.ExecutionContext.IsEvolutionReplay() || evolution.RunScore(run) != 2 || !h.runUsesAsset(run, asset) {
			return
		}
		seen[run.ID] = true
		succeeded = append(succeeded, run)
	}
	since := time.Now().UTC().AddDate(0, 0, -evoSampleWindowDays)
	for _, id := range h.repo.RunIDsPinnedTo(asset.Kind, asset.ID, since, 40) {
		if run, ok := h.repo.RunState(id); ok {
			consider(run)
		}
	}
	if len(succeeded) < evolution.MaxRegressionSamples {
		for _, run := range h.loadWindowRuns(p.ProjectID, evoSampleWindowDays, 60) {
			consider(run)
		}
	}
	return evolution.PlanSamples(failing, succeeded, pinned)
}

func (h *evolutionHandlers) runUsesAsset(run domain.Run, asset evoAsset) bool {
	if len(h.assetNodeIDs(run, asset)) > 0 {
		return true
	}
	if asset.Kind == domain.EvoAssetRule && asset.rule.Global {
		for _, id := range asset.rule.WorkflowIDs {
			if strings.TrimSpace(id) == run.WorkflowID {
				return true
			}
		}
	}
	return false
}

// assetNodeIDs lists the agent nodes of a run whose behavior the asset
// shapes: nodes running the role, or nodes whose role carries the rule.
func (h *evolutionHandlers) assetNodeIDs(run domain.Run, asset evoAsset) map[string]bool {
	out := map[string]bool{}
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.AgentID == "" || node.Type == "script" || node.Type == "command" || node.Type == "approval" || node.Type == "sop" {
			continue
		}
		if asset.Kind == domain.EvoAssetAgent && node.AgentID == asset.ID {
			out[node.ID] = true
		}
		if asset.Kind == domain.EvoAssetRule {
			if agent, ok := h.repo.Agent(node.AgentID); ok {
				for _, id := range agent.Rules {
					if id == asset.ID {
						out[node.ID] = true
					}
				}
			}
		}
	}
	if asset.Kind == domain.EvoAssetRule && asset.rule.Global && len(out) == 0 {
		for _, id := range asset.rule.WorkflowIDs {
			if strings.TrimSpace(id) != run.WorkflowID {
				continue
			}
			for _, node := range run.WorkflowSnapshot.Nodes {
				if node.AgentID != "" && node.Type != "approval" && node.Type != "script" && node.Type != "command" && node.Type != "sop" {
					out[node.ID] = true
				}
			}
		}
	}
	return out
}

// experienceLines combine released positive lessons with recent failed/rejected
// negative lessons of the same asset kind so the improver reuses proven edits
// and avoids repeating unsuccessful ideas.
func (h *evolutionHandlers) experienceLines(p domain.EvoProposal) []string {
	lines := []string{}
	for _, prior := range h.repo.EvoProposals(p.ProjectID, 30, domain.EvoProposalReleased) {
		if prior.AssetKind != p.AssetKind || prior.ID == p.ID {
			continue
		}
		lines = append(lines, "[有效经验] "+prior.Title+"："+domain.TrimEvoText(prior.Rationale, 180))
		if len(lines) >= 4 {
			break
		}
	}
	negCount := 0
	addNegative := func(prior domain.EvoProposal) bool {
		reason := firstNonEmpty(prior.DecisionNote, prior.Summary.Verdict, prior.Error)
		if strings.TrimSpace(reason) == "" || strings.TrimSpace(prior.Title) == "" || strings.HasPrefix(prior.Title, "提案生成失败") {
			return false
		}
		body := prior.Title
		if r := strings.TrimSpace(prior.Rationale); r != "" {
			body += "（" + domain.TrimEvoText(r, 90) + "）"
		}
		lines = append(lines, "[避免重复失败] "+body+" -> 未通过/被打回原因："+domain.TrimEvoText(reason, 120))
		negCount++
		return negCount >= 3
	}
	negatives := h.repo.EvoProposals(p.ProjectID, 30, domain.EvoProposalFailed, domain.EvoProposalRejected)
	seenNeg := map[string]bool{}
	for _, prior := range negatives {
		if prior.ID == p.ID || prior.AssetKind != p.AssetKind || prior.AssetID != p.AssetID {
			continue
		}
		seenNeg[prior.ID] = true
		if addNegative(prior) {
			return lines
		}
	}
	for _, prior := range negatives {
		if prior.ID == p.ID || seenNeg[prior.ID] || prior.AssetKind != p.AssetKind {
			continue
		}
		if addNegative(prior) {
			return lines
		}
	}
	return lines
}

// proposalPreferences returns the proposal's active SOP optimization priority
// chain when it originates from an SOP policy, or nil for non-SOP proposals.
func (h *evolutionHandlers) proposalPreferences(p domain.EvoProposal) []string {
	if len(p.Preferences) > 0 {
		return evolution.NormalizeSOPPreferences(p.Preferences)
	}
	if strings.TrimSpace(p.WorkflowID) != "" {
		pol := h.repo.EvoSOPPolicy(p.ProjectID, p.WorkflowID)
		return evolution.NormalizeSOPPreferences(pol.Preferences)
	}
	return nil
}

// resolveRefineActor re-authorizes the original initiator (or SOP policy owner)
// and resolves model credentials before launching an automatic reflection round.
func (h *evolutionHandlers) resolveRefineActor(p domain.EvoProposal) (domain.User, bool) {
	var wf domain.WorkflowDefinition
	if strings.TrimSpace(p.WorkflowID) != "" {
		if loaded, ok := h.repo.Workflow(p.WorkflowID); ok {
			wf = loaded
		}
	}
	if p.CreatedBy > 0 {
		user, ok := h.repo.User(p.CreatedBy)
		if !ok {
			return domain.User{}, false
		}
		isAdmin := strings.TrimSpace(user.LoginName) != "" && h.repo.IsPlatformAdmin(strings.TrimSpace(user.LoginName))
		if !isAdmin && !h.projectMemberWrites(p.ProjectID, user.ID) {
			return domain.User{}, false
		}
		resolvedUser, _, credOK := h.applySOPModelCredentials(context.Background(), user, p.Model, wf, evoAutoAllowSharedKey())
		if !credOK {
			return domain.User{}, false
		}
		return resolvedUser, true
	}
	if strings.TrimSpace(p.WorkflowID) != "" {
		pol := h.repo.EvoSOPPolicy(p.ProjectID, p.WorkflowID)
		if u, _, ok := h.resolveSOPActorUser(pol, wf); ok {
			return u, true
		}
	}
	return domain.User{}, false
}

// refineProposalJob runs one bounded reflection-and-refinement iteration after
// a proposal fails replay validation: it feeds the previous draft, verdict,
// diagnosis-only replay tool traces, and aggregate hidden-set stats back to the
// improver, then re-validates on the exact same replay sample plans.
func (h *evolutionHandlers) refineProposalJob(p domain.EvoProposal, prevReplays []domain.EvoReplay, user domain.User) {
	ctx, cancel := context.WithTimeout(context.Background(), evoProposalJobTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("evolution proposal %s refine panic: %v", p.ID, r)
			h.failProposal(p, fmt.Sprintf("反思修正任务异常：%v", r))
		}
	}()
	select {
	case h.jobs <- struct{}{}:
		defer func() { <-h.jobs }()
	case <-ctx.Done():
		h.failProposal(p, "反思修正排队超时")
		return
	}
	current, exists := h.repo.EvoProposal(p.ID)
	if !exists || current.Status != domain.EvoProposalDrafting {
		return
	}
	p = current
	asset, ok := h.loadAsset(p.AssetKind, p.AssetID)
	if !ok {
		h.failProposal(p, "资产已删除")
		return
	}
	if domain.ContentHash(asset.Content) != p.BaseHash {
		h.failProposal(p, "反思修正期间资产被修改过，请重新生成")
		return
	}
	finding, ok := h.repo.EvoFinding(p.FindingID)
	if !ok {
		h.failProposal(p, "原始诊断问题不存在，无法继续反思修正")
		return
	}

	plans := make([]evolution.SamplePlan, 0, len(prevReplays))
	critiques := make([]evolution.ReplayCritique, 0, len(prevReplays))
	diagEvidence := make([]evolution.Evidence, 0, len(prevReplays))
	hiddenTotal, hiddenRegressed, hiddenFixed := 0, 0, 0
	var hiddenBeforeTok, hiddenAfterTok int64
	hiddenTokCount := 0

	for _, r := range prevReplays {
		src, loaded := h.repo.RunState(r.SourceRunID)
		if loaded {
			if err := h.engine.ValidateEvolutionReplay(src); err == nil {
				plans = append(plans, evolution.SamplePlan{Group: r.Group, Run: src})
			}
		}
		if r.Group != domain.EvoGroupDiagnosis {
			if r.Outcome != domain.EvoOutcomeBlocked {
				hiddenTotal++
				switch r.Outcome {
				case domain.EvoOutcomeRegressed:
					hiddenRegressed++
				case domain.EvoOutcomeFixed:
					hiddenFixed++
				}
				if r.SourceMeasurements != nil && r.SourceMeasurements.Tokens != nil && *r.SourceMeasurements.Tokens > 0 &&
					r.ReplayMeasurements != nil && r.ReplayMeasurements.Tokens != nil && *r.ReplayMeasurements.Tokens > 0 {
					hiddenBeforeTok += *r.SourceMeasurements.Tokens
					hiddenAfterTok += *r.ReplayMeasurements.Tokens
					hiddenTokCount++
				}
			}
			continue
		}
		diagEvidence = append(diagEvidence, evolution.Evidence{RunID: r.SourceRunID})
		c := evolution.ReplayCritique{
			SourceRunID:  r.SourceRunID,
			ReplayRunID:  r.RunID,
			SourceStatus: r.SourceStatus,
			ReplayStatus: r.Status,
			Outcome:      r.Outcome,
			Error:        domain.TrimEvoText(r.Error, 400),
		}
		if r.SourceMeasurements != nil {
			if r.SourceMeasurements.Tokens != nil {
				c.SourceTokens = *r.SourceMeasurements.Tokens
			}
			if r.SourceMeasurements.ToolCalls != nil {
				c.SourceTools = *r.SourceMeasurements.ToolCalls
			}
			if r.SourceMeasurements.ExecutionMs != nil {
				c.SourceDurSec = float64(*r.SourceMeasurements.ExecutionMs) / 1000.0
			}
		}
		if r.ReplayMeasurements != nil {
			if r.ReplayMeasurements.Tokens != nil {
				c.ReplayTokens = *r.ReplayMeasurements.Tokens
			}
			if r.ReplayMeasurements.ToolCalls != nil {
				c.ReplayTools = *r.ReplayMeasurements.ToolCalls
			}
			if r.ReplayMeasurements.ExecutionMs != nil {
				c.ReplayDurSec = float64(*r.ReplayMeasurements.ExecutionMs) / 1000.0
			}
		}
		if r.RunID != "" {
			nodeID := ""
			for _, sig := range finding.Signals {
				if sig.RunID == r.SourceRunID && sig.NodeID != "" {
					nodeID = sig.NodeID
					break
				}
			}
			tools, _ := h.repo.SOPDetectionTools(r.RunID, 120)
			if len(tools) == 0 {
				if replayRun, ok := h.repo.RunState(r.RunID); ok {
					tools = replayRun.Telemetry
					if c.Error == "" && nodeID != "" && replayRun.Nodes[nodeID] != nil {
						c.Error = domain.TrimEvoText(replayRun.Nodes[nodeID].Error, 400)
					}
				}
			}
			c.ToolTrace = evolution.BuildToolTraceSummary(tools, nodeID)
		}
		critiques = append(critiques, c)
	}
	if len(plans) == 0 {
		h.failProposal(p, "反思修正缺少可回放样本")
		return
	}

	hiddenNote := ""
	if hiddenTotal > 0 {
		hiddenNote = fmt.Sprintf("隐藏留出/回归集共 %d 条：修好 %d，回归 %d", hiddenTotal, hiddenFixed, hiddenRegressed)
		if hiddenTokCount > 0 && hiddenBeforeTok > 0 {
			deltaPct := (float64(hiddenAfterTok-hiddenBeforeTok) / float64(hiddenBeforeTok)) * 100.0
			hiddenNote += fmt.Sprintf("，平均 Token 变化 %+.1f%%", deltaPct)
		}
		hiddenNote += "。"
	}

	prevCandidate := p.CandidateContent
	prevVerdict := ""
	if n := len(p.RefineHistory); n > 0 {
		prevVerdict = p.RefineHistory[n-1].Verdict
	}
	system, prompt := evolution.BuildRefinePrompt(
		asset.Kind, asset.Name, asset.Content, prevCandidate,
		finding, h.proposalPreferences(p), prevVerdict,
		critiques, hiddenNote, h.experienceLines(p),
	)
	raw, err := currentEvoImprove()(ctx, p, system, prompt, user)
	if err != nil {
		h.failProposal(p, "反思修正调用失败："+err.Error())
		return
	}
	candidate, err := evolution.ParseCandidate(raw)
	if err != nil {
		h.failProposal(p, "反思修正输出无效："+err.Error())
		return
	}
	if err := evolution.StaticCheck(asset.Content, candidate.Content); err != nil {
		h.failProposal(p, "反思修正静态检查未通过："+err.Error())
		return
	}
	if strings.TrimSpace(candidate.Content) == strings.TrimSpace(prevCandidate) {
		h.failProposal(p, "反思修正未能产出与上轮不同的候选正文："+prevVerdict)
		return
	}
	if latest, ok := h.loadAsset(p.AssetKind, p.AssetID); !ok || domain.ContentHash(latest.Content) != p.BaseHash {
		h.failProposal(p, "反思修正期间资产被修改过，请重新生成")
		return
	}
	if current, ok := h.repo.EvoProposal(p.ID); ok {
		if current.Status != domain.EvoProposalDrafting {
			return
		}
		p = current
	}
	if err := h.repo.DeleteEvoReplays(p.ID); err != nil {
		h.failProposal(p, "清理上轮回放记录失败："+err.Error())
		return
	}
	p.Title, p.Rationale, p.CandidateContent = candidate.Title, candidate.Rationale, candidate.Content
	p.Review = evolution.CandidateReview(candidate, p.BaseContent, diagEvidence)
	p.Summary = domain.EvoReplaySummary{}
	p.ExpectedReplays = len(plans)
	p.Status = domain.EvoProposalValidating
	if err := h.repo.PutEvoProposal(p); err != nil {
		h.failProposal(p, err.Error())
		return
	}
	overrides := domain.EvoAssetOverride{}
	if asset.Kind == domain.EvoAssetAgent {
		overrides.AgentPrompts = map[string]string{asset.ID: candidate.Content}
	} else {
		overrides.RuleContents = map[string]string{asset.ID: candidate.Content}
	}
	for _, plan := range plans {
		h.startReplay(p, plan, asset, overrides, user)
	}
	h.sweepProposal(p, time.Now().UTC())
}

func (h *evolutionHandlers) startReplay(p domain.EvoProposal, plan evolution.SamplePlan, asset evoAsset, overrides domain.EvoAssetOverride, user domain.User) {
	current, ok := h.repo.EvoProposal(p.ID)
	if !ok || current.Status != domain.EvoProposalValidating {
		return
	}
	source := plan.Run
	now := time.Now().UTC()
	replay := domain.EvoReplay{
		ID: "evr-" + randomID(), ProposalID: p.ID, ProjectID: p.ProjectID, Group: plan.Group,
		SourceRunID: source.ID, SourceStatus: source.Status, SourceScore: evolution.RunScore(source),
		Status: "pending", Overrides: overrides, StartedAt: now,
	}
	if err := h.repo.PutEvoReplay(replay); err != nil {
		log.Printf("evolution replay %s persist: %v", replay.ID, err)
		return
	}
	exec := domain.TelemetryContext{
		ProjectID:         p.ProjectID,
		ResponsibleUserID: user.ID,
		Purpose:           domain.RunPurposeEvolutionReplay,
		EvolutionReplayID: replay.ID,
	}
	run, reused, err := h.engine.StartEvolutionReplay(source, evolution.ReplayRepositories(source, replay.ID), exec, h.assetNodeIDs(source, asset))
	if err != nil {
		replay.Status = "error"
		replay.Outcome = domain.EvoOutcomeBlocked
		replay.Error = "回放启动失败：" + err.Error()
		replay.FinishedAt = time.Now().UTC()
	} else {
		replay.RunID = run.ID
		replay.Status = firstNonEmpty(run.Status, "running")
		replay.ReusedNodes = reused
		replay.SourceMeasurements = evoReplayMeasurements(source, reused)
		current, exists := h.repo.EvoProposal(p.ID)
		if !exists || current.Status != domain.EvoProposalValidating {
			_ = h.engine.Cancel(run.ID)
			blockReplay(&replay, time.Now().UTC(), "提案已结束，回放已停止")
		}
	}
	if err := h.repo.PutEvoReplay(replay); err != nil {
		log.Printf("evolution replay %s persist: %v", replay.ID, err)
	}
}

// ---------- replay monitor ----------

func (h *evolutionHandlers) startReplayMonitor() {
	go func() {
		h.maybeSweepSOPBatches(time.Now().UTC())
		ticker := time.NewTicker(evoReplaySweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now().UTC()
			h.sweepReplays(now)
			h.maybeSweepTrials(now)
			h.maybeSweepSOPBatches(now)
		}
	}()
}

func (h *evolutionHandlers) sweepReplays(now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("evolution replay sweep panic: %v", r)
		}
	}()
	for _, p := range h.repo.EvoProposals("", 100, domain.EvoProposalDrafting) {
		if now.Sub(p.UpdatedAt) > evoProposalJobTimeout+time.Minute {
			h.failProposal(p, "提案生成已超时或执行进程已中断，请手动重试")
		}
	}
	for _, p := range h.repo.EvoProposals("", 100, domain.EvoProposalValidating) {
		h.sweepProposal(p, now)
	}
}

func blockReplay(r *domain.EvoReplay, now time.Time, msg string) {
	r.Outcome = domain.EvoOutcomeBlocked
	r.Error = msg
	r.FinishedAt = now
}

// evoFeedbackSampleGroup: a diagnosis/holdout sample whose source run had
// succeeded, i.e. it is in the finding only because of feedback or evals.
func evoFeedbackSampleGroup(r domain.EvoReplay) bool {
	return (r.Group == domain.EvoGroupDiagnosis || r.Group == domain.EvoGroupHoldout) && r.SourceScore == 2
}

func evoSnapshotNode(run domain.Run, id string) (domain.WorkflowNode, bool) {
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return domain.WorkflowNode{}, false
}

func evoNodeName(run domain.Run, id string) string {
	if node, ok := evoSnapshotNode(run, id); ok && strings.TrimSpace(node.Name) != "" {
		return node.Name
	}
	return id
}

// deliberateAssetVerdict finds a node shaped by the asset that stopped the
// run by its own decision (submit_gate_result fail / rework), as opposed to
// crashing or erroring: either the node failed on it, or it is parked on a
// human gate because its rework budget ran out.
func (h *evolutionHandlers) deliberateAssetVerdict(run domain.Run, asset evoAsset) (string, string) {
	for id := range h.assetNodeIDs(run, asset) {
		nr := run.Nodes[id]
		if nr == nil || (nr.Status != "failed" && nr.Status != "waiting_approval") || nr.GateResult == nil {
			continue
		}
		if d := nr.GateResult.Decision; d != "fail" && d != "rework" {
			continue
		}
		return evoNodeName(run, id), firstNonEmpty(nr.GateResult.Reason, nr.GateResult.Summary)
	}
	return "", ""
}

// assetRework describes a rework a node shaped by the asset sent upstream,
// e.g. a reviewer that now rejects design.md once before passing it.
func (h *evolutionHandlers) assetRework(run domain.Run, asset evoAsset) string {
	for id := range h.assetNodeIDs(run, asset) {
		node, ok := evoSnapshotNode(run, id)
		if !ok || node.RejectTo == "" {
			continue
		}
		if target := run.Nodes[node.RejectTo]; target != nil && target.ReworkCount > 0 {
			return fmt.Sprintf("「%s」按新正文打回「%s」返工 %d 次后通过", evoNodeName(run, id), evoNodeName(run, node.RejectTo), target.ReworkCount)
		}
	}
	return ""
}

// autoApproveReplayGates lets a replay pass human gates the source run had
// already passed: approval nodes and upstream agents that now ask for a
// confirmation. Replays never deliver or write externally, and the gate is
// only answered when the source run continued past the same node. A gate
// raised by a node the asset shapes stays open: that is the candidate's own
// behavior and the wait grace turns it into a blocked sample.
func (h *evolutionHandlers) autoApproveReplayGates(r *domain.EvoReplay, run domain.Run, asset evoAsset) bool {
	source, ok := h.repo.RunState(r.SourceRunID)
	if !ok {
		return false
	}
	shaped := h.assetNodeIDs(run, asset)
	approved := false
	for id, nr := range run.Nodes {
		if nr == nil || nr.Status != "waiting_approval" {
			continue
		}
		node, found := evoSnapshotNode(run, id)
		if !found || (shaped[id] && node.Type != "approval") {
			continue
		}
		if src := source.Nodes[id]; src == nil || src.Status != "succeeded" {
			continue
		}
		note := "进化回放自动确认：原运行在此节点已继续执行。请沿用需求中的信息和常规默认做法继续，不要再等待人工补充。"
		if err := h.engine.Approve(run.ID, id, note); err != nil {
			log.Printf("evolution replay %s auto-approve %s: %v", r.ID, id, err)
			continue
		}
		approved = true
	}
	return approved
}

// sweepProposal advances replays from their run states and settles the
// proposal once every replay has an outcome: passed → pending (awaiting a
// human), otherwise failed with the verdict.
func (h *evolutionHandlers) sweepProposal(p domain.EvoProposal, now time.Time) domain.EvoProposal {
	if current, ok := h.repo.EvoProposal(p.ID); ok {
		p = current
	}
	if p.Status != domain.EvoProposalValidating {
		return p
	}
	timeout := evoDurationEnv("AGENTFLOW_EVOLUTION_REPLAY_TIMEOUT", evoDefaultReplayTimeout)
	grace := evoDurationEnv("AGENTFLOW_EVOLUTION_WAIT_GRACE", evoDefaultWaitGrace)
	asset, assetOK := h.loadAsset(p.AssetKind, p.AssetID)
	sopAssertions := h.proposalSOPAssertions(p)
	sopBaseline := h.proposalSOPBaseline(p)
	replays := h.repo.EvoReplays(p.ID)
	if len(replays) < p.ExpectedReplays {
		if now.Sub(p.UpdatedAt) > timeout {
			p.Status = domain.EvoProposalFailed
			p.Error = "回放样本未全部创建，已超时停止"
			h.failProposal(p, p.Error)
		}
		return p
	}
	for i := range replays {
		r := &replays[i]
		if r.Outcome != "" {
			continue
		}
		before := *r
		switch {
		case r.RunID == "":
			if now.Sub(r.StartedAt) > evoReplayStartGrace {
				blockReplay(r, now, "回放没有启动")
			}
		default:
			run, ok := h.repo.RunState(r.RunID)
			if !ok {
				blockReplay(r, now, "回放运行已被删除")
				break
			}
			r.Status = run.Status
			if score := evolution.RunScore(run); score >= 0 {
				r.Score = score
				r.ReplayMeasurements = evoReplayMeasurements(run, r.ReusedNodes)
				r.FinishedAt = now
				if run.Status == "cancelled" {
					blockReplay(r, now, firstNonEmpty(r.Error, "回放运行被取消"))
					break
				}
				// Layer 2 hard gate: evaluate SOP outcome assertions first.
				if len(sopAssertions) > 0 {
					rep := evolution.EvaluateRunAssertionsForSource(run, r.SourceRunID, sopAssertions)
					r.AssertionReport = &rep
					if rep.Configured && !rep.AllPassed {
						r.Score = 0
						r.Outcome = domain.EvoOutcomeRegressed
						r.Error = "结果验收未通过：" + formatFailedAssertionReasons(rep)
						break
					}
				}
				// Layer 1 deterministic process score for paired comparison.
				repPS := evolution.ScoreRunProcess(run, sopBaseline)
				r.ReplayProcessScore = &repPS
				if srcRun, okSrc := h.repo.RunState(r.SourceRunID); okSrc {
					srcPS := evolution.ScoreRunProcess(srcRun, sopBaseline)
					r.SourceProcessScore = &srcPS
				}
				r.Outcome = evolution.Outcome(r.SourceScore, score)
				if assetOK && r.Outcome == domain.EvoOutcomeRegressed && evoFeedbackSampleGroup(*r) {
					// The improved node itself changed the run's course (a
					// reviewer now REJECTs, or sends work back before passing
					// it). For a sample raised by feedback that is the behavior
					// change under test, not a defect; a human judges it from
					// the replay output.
					if node, verdict := h.deliberateAssetVerdict(run, asset); node != "" {
						r.Outcome = domain.EvoOutcomeChanged
						r.Error = "「" + node + "」按新正文给出阻断结论：" + domain.TrimEvoText(verdict, 160)
					} else if note := h.assetRework(run, asset); note != "" && run.Status == "succeeded" {
						r.Outcome = domain.EvoOutcomeChanged
						r.Error = note
					}
				}
				break
			}
			if evolution.ReplayWaiting(run) {
				if assetOK && evoFeedbackSampleGroup(*r) {
					if node, verdict := h.deliberateAssetVerdict(run, asset); node != "" {
						// Rework budget spent and the improved node still
						// rejects: its verdict is final for this replay.
						_ = h.engine.Cancel(run.ID)
						r.Status = "cancelled"
						r.Score = 0
						r.FinishedAt = now
						r.WaitingSince = time.Time{}
						r.Outcome = domain.EvoOutcomeChanged
						r.Error = "「" + node + "」按新正文返工后仍给出阻断结论：" + domain.TrimEvoText(verdict, 160)
						break
					}
				}
				if assetOK && h.autoApproveReplayGates(r, run, asset) {
					r.Status = "running"
					r.WaitingSince = time.Time{}
					break
				}
				r.Status = "waiting"
				if r.WaitingSince.IsZero() {
					r.WaitingSince = now
				} else if now.Sub(r.WaitingSince) > grace {
					_ = h.engine.Cancel(run.ID)
					blockReplay(r, now, "回放停在人工节点超过 "+grace.String()+"，已停止")
				}
				break
			}
			r.WaitingSince = time.Time{}
			if !r.StartedAt.IsZero() && now.Sub(r.StartedAt) > timeout {
				_ = h.engine.Cancel(run.ID)
				blockReplay(r, now, "回放超过 "+timeout.String()+"，已停止")
			}
		}
		if before.Status != r.Status || before.Outcome != r.Outcome || !before.WaitingSince.Equal(r.WaitingSince) {
			if err := h.repo.PutEvoReplay(*r); err != nil {
				log.Printf("evolution replay %s persist: %v", r.ID, err)
			}
		}
	}
	summary := evolution.SummarizeWithPreferences(replays, h.proposalPreferences(p))
	changed := summary != p.Summary
	p.Summary = summary
	launchRefine := false
	var refineActor domain.User
	if summary.Total == 0 || summary.Finished == summary.Total {
		changed = true
		if summary.Passed {
			p.Status = domain.EvoProposalPending
		} else if summary.Total > summary.Blocked &&
			p.RefineRound < evolution.MaxRefineRounds &&
			assetOK && domain.ContentHash(asset.Content) == p.BaseHash &&
			strings.TrimSpace(p.FindingID) != "" {
			if budget := h.projectBudgetStatus(p.ProjectID, now); budget.Exceeded {
				p.Status = domain.EvoProposalFailed
				p.Error = summary.Verdict + "（" + budget.Reason + "，已跳过自动反思修正）"
			} else if actor, ok := h.resolveRefineActor(p); ok {
				p.RefineHistory = append(p.RefineHistory, domain.EvoRefineAttempt{
					Round:            p.RefineRound + 1,
					Title:            p.Title,
					Rationale:        p.Rationale,
					Verdict:          summary.Verdict,
					CandidateContent: p.CandidateContent,
					Summary:          summary,
					FinishedAt:       now,
				})
				p.RefineRound++
				p.Status = domain.EvoProposalDrafting
				p.Summary = domain.EvoReplaySummary{}
				p.Error = ""
				launchRefine = true
				refineActor = actor
			} else {
				p.Status = domain.EvoProposalFailed
				p.Error = summary.Verdict
			}
		} else {
			p.Status = domain.EvoProposalFailed
			p.Error = summary.Verdict
		}
	}
	if changed {
		if err := h.repo.PutEvoProposal(p); err != nil {
			log.Printf("evolution proposal %s persist: %v", p.ID, err)
			stored, _ := h.repo.EvoProposal(p.ID)
			return stored
		}
		p, _ = h.repo.EvoProposal(p.ID)
		if launchRefine {
			replaysCopy := append([]domain.EvoReplay(nil), replays...)
			go h.refineProposalJob(p, replaysCopy, refineActor)
			return p
		}
		if p.Status == domain.EvoProposalFailed {
			h.recordSOPProposalOutcome(p, false, p.Error, now)
		}
		if p.Status == domain.EvoProposalPending && p.SemanticJudge == nil && p.Summary.AssertionsFailed == 0 && p.Summary.ProcessDelta > -5 && p.Summary.ProcessDelta < 5 {
			if actor, ok := h.resolveRefineActor(p); ok {
				replaysCopy := append([]domain.EvoReplay(nil), replays...)
				go h.attachSemanticJudge(p.ID, replaysCopy, actor)
			}
		}
		if p.Status == domain.EvoProposalPending && assetOK && h.autoEvolution(asset) {
			if allowed, reason := h.proposalAllowsAutoRelease(p); !allowed {
				p.AutoReleaseBlocked = reason
				_ = h.repo.PutEvoProposal(p)
			} else if started, err := h.startCanary(p, asset, 0, 0, 0, "L3 自动开始试用", true); err == nil {
				started.Replays = replays
				return started
			} else {
				log.Printf("evolution proposal %s auto trial: %v", p.ID, err)
			}
		}
	}
	p.Replays = replays
	return p
}

func (h *evolutionHandlers) cancelReplays(p domain.EvoProposal) {
	now := time.Now().UTC()
	for _, r := range h.repo.EvoReplays(p.ID) {
		if r.Outcome != "" {
			continue
		}
		if r.RunID != "" {
			_ = h.engine.Cancel(r.RunID)
		}
		blockReplay(&r, now, "提案已打回，回放已停止")
		_ = h.repo.PutEvoReplay(r)
	}
}

// ---------- release ----------

func (h *evolutionHandlers) releaseProposal(p domain.EvoProposal, asset evoAsset, userID int64, note string, force bool) (domain.EvoProposal, error) {
	if err := privateEvolutionAsset(asset, p.ProjectID); err != nil {
		return p, err
	}
	if p.Status == domain.EvoProposalCanary && (p.Canary == nil || p.Canary.Status != domain.EvoCanaryPassed) {
		return p, evoConflict("灰度尚未通过，不能发布")
	}
	watchMinRuns := evolution.DefaultWatchMinRuns
	if sopRuns := h.proposalSOPWatchHealthyRuns(p); sopRuns > 0 && sopRuns < watchMinRuns {
		watchMinRuns = sopRuns
	}
	released, err := h.repo.ReleaseEvoProposal(p, userID, note, force, watchMinRuns)
	if err == nil {
		h.recordSOPProposalOutcome(released, true, "", time.Now().UTC())
	}
	return released, err
}

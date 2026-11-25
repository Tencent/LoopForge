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
	}
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
			evidence = append(evidence, evolution.BuildEvidence(plan.Run, finding))
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

// experienceLines are released proposals of the same kind in the project:
// the experience library the improver may learn from.
func (h *evolutionHandlers) experienceLines(p domain.EvoProposal) []string {
	lines := []string{}
	for _, prior := range h.repo.EvoProposals(p.ProjectID, 30, domain.EvoProposalReleased) {
		if prior.AssetKind != p.AssetKind || prior.ID == p.ID {
			continue
		}
		lines = append(lines, prior.Title+"："+domain.TrimEvoText(prior.Rationale, 200))
		if len(lines) >= 5 {
			break
		}
	}
	return lines
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
	summary := evolution.Summarize(replays)
	changed := summary != p.Summary
	p.Summary = summary
	if summary.Total == 0 || summary.Finished == summary.Total {
		changed = true
		if summary.Passed {
			p.Status = domain.EvoProposalPending
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
		if p.Status == domain.EvoProposalPending && assetOK && h.autoEvolution(asset) {
			if started, err := h.startCanary(p, asset, 0, 0, 0, "L3 自动开始试用", true); err == nil {
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
	return h.repo.ReleaseEvoProposal(p, userID, note, force, evolution.DefaultWatchMinRuns)
}

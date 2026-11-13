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

var errUnsupportedEvoAsset = errors.New("unsupported evolution asset kind")

// evoError carries an HTTP status for flow errors (conflicts, bad state).
type evoError struct {
	status int
	msg    string
}

func (e *evoError) Error() string { return e.msg }

func evoConflict(msg string) error { return &evoError{status: http.StatusConflict, msg: msg} }

func respondEvoError(c *gin.Context, err error) {
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
		raw, _, err := runAssistantCLI(ctx, assistantCLIRequest{Prompt: prompt, Model: p.Model, SystemPrompt: system, UserID: user.ID}, user, func(assistantStreamEvent) {})
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
	if f, ok := h.repo.OpenEvoFindingForAsset(projectID, asset.Kind, asset.ID); ok {
		return f, true
	}
	days := evoDefaultWindowDays
	runs := h.loadWindowRuns(projectID, days, evoMaxDiagnoseRuns)
	evals := map[string]domain.RunEvalRecord{}
	for _, run := range runs {
		if rec, ok := h.repo.LatestRunEval(run.ID); ok && rec.Status == domain.RunEvalSucceeded {
			evals[run.ID] = rec
		}
	}
	found := evolution.Diagnose(evolution.DiagnoseInput{
		ProjectID: projectID, Runs: runs, Evals: evals,
		Feedback: h.repo.EvoSignals(projectID, time.Now().UTC().AddDate(0, 0, -days)),
		Agent:    h.evolvableAgent, Rule: h.repo.Rule,
		Level: func(kind, id string) int {
			if kind == asset.Kind && id == asset.ID {
				return domain.EvoLevelPropose
			}
			return domain.EvoLevelOff
		},
	})
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
	if current, ok := h.repo.EvoProposal(p.ID); ok {
		p = current
	}
	p.Status = domain.EvoProposalFailed
	p.Error = msg
	if strings.HasPrefix(p.Title, "正在生成") {
		p.Title = "提案生成失败"
	}
	if err := h.repo.PutEvoProposal(p); err != nil {
		log.Printf("evolution proposal %s fail persist: %v", p.ID, err)
	}
	h.reopenFinding(p)
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
	asset, ok := h.loadAsset(p.AssetKind, p.AssetID)
	if !ok {
		h.failProposal(p, "资产已删除")
		return
	}
	plans := h.planReplays(p, finding, asset)
	evidence := []evolution.Evidence{}
	for _, plan := range plans {
		if plan.Group == domain.EvoGroupDiagnosis {
			evidence = append(evidence, evolution.BuildEvidence(plan.Run, finding))
		}
	}
	if len(evidence) == 0 {
		h.failProposal(p, "没有可回放的失败样本（样本运行可能已删除或不属于可回放的 SOP）")
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
		h.startReplay(p, plan, overrides, user)
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
		if !ok || run.ExecutionContext.IsEvolutionReplay() {
			continue
		}
		if score := evolution.RunScore(run); score == 0 || score == 1 {
			failing = append(failing, run)
		}
	}
	pinned := []domain.Run{}
	for _, sample := range h.repo.EvoSamples(p.ProjectID) {
		run, ok := h.repo.RunState(sample.RunID)
		if ok && h.runUsesAsset(run, asset) && evolution.RunScore(run) == 2 {
			pinned = append(pinned, run)
		}
	}
	succeeded := []domain.Run{}
	seen := map[string]bool{}
	consider := func(run domain.Run) {
		if seen[run.ID] || run.ExecutionContext.IsEvolutionReplay() || evolution.RunScore(run) != 2 || !h.runUsesAsset(run, asset) {
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
	for _, node := range run.WorkflowSnapshot.Nodes {
		if node.AgentID == "" || node.Type == "script" || node.Type == "command" || node.Type == "approval" || node.Type == "sop" {
			continue
		}
		if asset.Kind == domain.EvoAssetAgent && node.AgentID == asset.ID {
			return true
		}
		if asset.Kind == domain.EvoAssetRule {
			if agent, ok := h.repo.Agent(node.AgentID); ok {
				for _, id := range agent.Rules {
					if id == asset.ID {
						return true
					}
				}
			}
		}
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

func (h *evolutionHandlers) startReplay(p domain.EvoProposal, plan evolution.SamplePlan, overrides domain.EvoAssetOverride, user domain.User) {
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
	run, err := h.engine.StartWithRepositoriesContext(source.WorkflowID, source.Input, evolution.ReplayRepositories(source, replay.ID), exec)
	if err != nil {
		replay.Status = "error"
		replay.Outcome = domain.EvoOutcomeBlocked
		replay.Error = "回放启动失败：" + err.Error()
		replay.FinishedAt = time.Now().UTC()
	} else {
		replay.RunID = run.ID
		replay.Status = firstNonEmpty(run.Status, "running")
	}
	if err := h.repo.PutEvoReplay(replay); err != nil {
		log.Printf("evolution replay %s persist: %v", replay.ID, err)
	}
}

// ---------- replay monitor ----------

func (h *evolutionHandlers) startReplayMonitor() {
	go func() {
		ticker := time.NewTicker(evoReplaySweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.sweepReplays(time.Now().UTC())
		}
	}()
}

func (h *evolutionHandlers) sweepReplays(now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("evolution replay sweep panic: %v", r)
		}
	}()
	for _, p := range h.repo.EvoProposals("", 100, domain.EvoProposalValidating) {
		h.sweepProposal(p, now)
	}
}

func blockReplay(r *domain.EvoReplay, now time.Time, msg string) {
	r.Outcome = domain.EvoOutcomeBlocked
	r.Error = msg
	r.FinishedAt = now
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
	replays := h.repo.EvoReplays(p.ID)
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
				r.FinishedAt = now
				if run.Status == "cancelled" {
					blockReplay(r, now, firstNonEmpty(r.Error, "回放运行被取消"))
				} else {
					r.Outcome = evolution.Outcome(r.SourceScore, score)
				}
				break
			}
			if evolution.ReplayWaiting(run) {
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
		}
		if p.Status == domain.EvoProposalFailed {
			h.reopenFinding(p)
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
	switch {
	case p.Status == domain.EvoProposalPending:
	case force && p.Status == domain.EvoProposalFailed && strings.TrimSpace(p.CandidateContent) != "":
	default:
		return p, evoConflict("提案当前状态不能发布")
	}
	if !p.Summary.Passed && !force {
		return p, evoConflict("回放验证未通过，不能发布")
	}
	if domain.ContentHash(asset.Content) != p.BaseHash {
		return p, evoConflict("资产在提案生成后被修改过，请打回后重新生成")
	}
	if _, err := h.ensureVersioned(asset, userID); err != nil {
		return p, err
	}
	if err := h.writeAssetContent(asset, p.CandidateContent); err != nil {
		return p, err
	}
	version, err := h.repo.AppendAssetVersion(domain.AssetVersion{
		AssetKind: asset.Kind, AssetID: asset.ID, Content: p.CandidateContent,
		Source: domain.EvoVersionEvolution, ProposalID: p.ID, Note: p.Title, CreatedBy: userID,
	})
	if err != nil {
		return p, err
	}
	p.Status = domain.EvoProposalReleased
	p.ReleasedVersion = version.Version
	p.DecidedBy = userID
	p.DecidedAt = time.Now().UTC()
	p.DecisionNote = note
	if force && !p.Summary.Passed {
		p.DecisionNote = strings.TrimSpace("平台管理员强制发布。" + note)
	}
	if err := h.repo.PutEvoProposal(p); err != nil {
		return p, err
	}
	if p.FindingID != "" {
		if f, ok := h.repo.EvoFinding(p.FindingID); ok {
			f.Status = domain.EvoFindingResolved
			_ = h.repo.PutEvoFinding(f)
		}
	}
	stored, _ := h.repo.EvoProposal(p.ID)
	return stored, nil
}

package httpapi

import (
	"log"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
)

// Trials and post-release watches.
//
// A proposal that passed replay can start a trial: a share of new business
// runs that use the asset start with the candidate (canary arm), the rest
// keep the live body (control arm). The engine assigns the arm at run start
// and freezes it in the run's asset pins, so the sweeper splits runs by pin
// hash and compares how the asset's nodes ended in each arm. A released
// version is then watched against the version it replaced; a degradation is
// flagged (L2) or rolled back (L3).

const (
	evoTrialSweepInterval = 2 * time.Minute
	evoTrialScoreCacheMax = 5000
)

// startCanary moves a passed proposal into a trial. The catalog asset is not
// touched; release happens on promotion.
func (h *evolutionHandlers) startCanary(p domain.EvoProposal, asset evoAsset, userID int64, percent, minRuns int, note string, auto bool) (domain.EvoProposal, error) {
	if err := privateEvolutionAsset(asset, p.ProjectID); err != nil {
		return p, err
	}
	if auto && !h.autoEvolution(asset) {
		return p, evoConflict("自动进化尚未启用，请使用人工确认的 L2 流程")
	}
	if p.Status != domain.EvoProposalPending || !p.Summary.Passed {
		return p, evoConflict("只有回放通过、等待确认的提案可以开始试用")
	}
	if strings.TrimSpace(p.CandidateContent) == "" {
		return p, evoConflict("提案没有候选正文")
	}
	if domain.ContentHash(asset.Content) != p.BaseHash {
		return p, evoConflict("资产在提案生成后被修改过，请打回后重新生成")
	}
	percent, minRuns = evolution.ClampCanary(percent, minRuns)
	now := time.Now().UTC()
	p.Status = domain.EvoProposalCanary
	p.Canary = &domain.EvoCanary{Percent: percent, MinRuns: minRuns, Status: domain.EvoCanaryRunning, Auto: auto, StartedBy: userID, StartedAt: now,
		Verdict: "试用中，每组需要 " + strconv.Itoa(minRuns) + " 次运行"}
	if note = strings.TrimSpace(note); note != "" {
		p.DecisionNote = note
	}
	if err := h.repo.PutEvoProposal(p); err != nil {
		return p, err
	}
	stored, _ := h.repo.EvoProposal(p.ID)
	return stored, nil
}

// maybeSweepTrials runs the trial / watch sweep at most every
// evoTrialSweepInterval; it loads run states, so it is slower than replays.
func (h *evolutionHandlers) maybeSweepTrials(now time.Time) {
	h.trialM.Lock()
	due := now.Sub(h.trialAt) >= evoTrialSweepInterval
	if due {
		h.trialAt = now
	}
	h.trialM.Unlock()
	if due {
		h.sweepTrials(now)
	}
}

func (h *evolutionHandlers) sweepTrials(now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("evolution trial sweep panic: %v", r)
		}
	}()
	for _, p := range h.repo.EvoProposals("", 100, domain.EvoProposalCanary) {
		h.sweepCanary(p, now)
	}
	for _, p := range h.repo.EvoProposals("", 200, domain.EvoProposalReleased) {
		if p.Watch.Active() {
			h.sweepWatch(p, now)
		}
	}
}

// trialScore grades one business run for the asset; terminal results are
// cached because a finished run never changes.
func (h *evolutionHandlers) trialScore(projectID, runID string, asset evoAsset) (int, bool) {
	key := projectID + ":" + asset.Kind + ":" + asset.ID + ":" + runID
	h.trialM.Lock()
	score, cached := h.trialScores[key]
	h.trialM.Unlock()
	if cached {
		return score, score >= 0
	}
	run, ok := h.repo.RunState(runID)
	if !ok {
		return 0, false
	}
	if run.ExecutionContext.IsEvolutionReplay() || h.runProject(run) != projectID {
		h.cacheTrialScore(key, -1)
		return 0, false
	}
	score, ok = evolution.AssetRunScore(run, h.assetNodeIDs(run, asset))
	if domain.TerminalRunStatus(run.Status) {
		if !ok {
			h.cacheTrialScore(key, -1)
		} else {
			h.cacheTrialScore(key, score)
		}
	}
	return score, ok
}

func (h *evolutionHandlers) cacheTrialScore(key string, score int) {
	h.trialM.Lock()
	defer h.trialM.Unlock()
	if len(h.trialScores) >= evoTrialScoreCacheMax {
		h.trialScores = map[string]int{}
	}
	h.trialScores[key] = score
}

// armStats scores the pinned runs whose start body has the given hash.
func (h *evolutionHandlers) armStats(projectID string, pins []domain.RunAssetPin, hash string, asset evoAsset, limit int) domain.EvoArmStats {
	stats := domain.EvoArmStats{}
	for _, pin := range pins {
		if pin.Hash != hash {
			continue
		}
		if limit > 0 && stats.Runs >= limit {
			break
		}
		if score, ok := h.trialScore(projectID, pin.RunID, asset); ok {
			evolution.AddScore(&stats, score)
		}
	}
	return stats
}

func (h *evolutionHandlers) endCanary(p domain.EvoProposal, now time.Time, canaryStatus, verdict string) {
	p.Canary.Status = canaryStatus
	p.Canary.Verdict = verdict
	p.Canary.EndedAt = now
	p.Canary.CheckedAt = now
	p.Status = domain.EvoProposalFailed
	p.Error = verdict
	if err := h.repo.PutEvoProposal(p); err != nil {
		log.Printf("evolution trial %s persist: %v", p.ID, err)
		return
	}
}

func (h *evolutionHandlers) sweepCanary(p domain.EvoProposal, now time.Time) domain.EvoProposal {
	if p.Canary == nil || !p.TrialActive() {
		return p
	}
	asset, ok := h.loadAsset(p.AssetKind, p.AssetID)
	if !ok {
		h.endCanary(p, now, domain.EvoCanaryAborted, "资产已删除，试用已终止")
		return p
	}
	if privateEvolutionAsset(asset, p.ProjectID) != nil || domain.ContentHash(asset.Content) != p.BaseHash {
		h.endCanary(p, now, domain.EvoCanaryAborted, "试用期间资产在进化流程外被修改，试用已终止，请重新生成提案")
		return p
	}
	before := *p.Canary
	pins := h.repo.AssetPinsBetween(asset.Kind, asset.ID, p.Canary.StartedAt, time.Time{}, evolution.TrialRunScanLimit)
	p.Canary.Canary = h.armStats(p.ProjectID, pins, domain.ContentHash(p.CandidateContent), asset, 0)
	p.Canary.Control = h.armStats(p.ProjectID, pins, p.BaseHash, asset, 0)
	p.Canary.ProbWorse = evolution.ProbWorse(p.Canary.Canary, p.Canary.Control)
	status, verdict := evolution.JudgeCanary(*p.Canary)
	p.Canary.Verdict = verdict
	if status == domain.EvoCanaryFailed {
		h.endCanary(p, now, domain.EvoCanaryFailed, verdict)
		return p
	}
	if status == domain.EvoCanaryPassed {
		p.Canary.Status = domain.EvoCanaryPassed
	}
	after := *p.Canary
	after.CheckedAt = before.CheckedAt
	if after != before {
		p.Canary.CheckedAt = now
		if err := h.repo.PutEvoProposal(p); err != nil {
			log.Printf("evolution trial %s persist: %v", p.ID, err)
			return p
		}
		p, _ = h.repo.EvoProposal(p.ID)
	}
	if p.Canary.Status == domain.EvoCanaryPassed && h.autoEvolution(asset) {
		released, err := h.releaseProposal(p, asset, 0, "L3 自动全量："+verdict, false)
		if err != nil {
			log.Printf("evolution trial %s auto release: %v", p.ID, err)
			return p
		}
		return released
	}
	return p
}

func (h *evolutionHandlers) sweepWatch(p domain.EvoProposal, now time.Time) domain.EvoProposal {
	w := p.Watch
	if !w.Active() {
		return p
	}
	save := func() bool {
		if err := h.repo.PutEvoProposal(p); err != nil {
			log.Printf("evolution watch %s persist: %v", p.ID, err)
			return false
		}
		p, _ = h.repo.EvoProposal(p.ID)
		w = p.Watch
		return true
	}
	asset, ok := h.loadAsset(p.AssetKind, p.AssetID)
	if !ok || privateEvolutionAsset(asset, p.ProjectID) != nil || domain.ContentHash(asset.Content) != w.Hash {
		w.Status, w.Verdict, w.EndedAt, w.CheckedAt = domain.EvoWatchEnded, "资产已被修改、回滚或删除，停止观察", now, now
		save()
		return p
	}
	before := *w
	if !w.BaselineReady {
		since := w.StartedAt.AddDate(0, 0, -evolution.WatchBaselineDays)
		pins := h.repo.AssetPinsBetween(asset.Kind, asset.ID, since, w.StartedAt, evolution.TrialRunScanLimit)
		w.Baseline = h.armStats(p.ProjectID, pins, w.PrevHash, asset, evolution.WatchBaselineRuns)
		w.BaselineReady = true
	}
	pins := h.repo.AssetPinsBetween(asset.Kind, asset.ID, w.StartedAt, time.Time{}, evolution.TrialRunScanLimit)
	w.Current = h.armStats(p.ProjectID, pins, w.Hash, asset, 0)
	w.ProbWorse = evolution.ProbWorse(w.Current, w.Baseline)
	status, verdict := evolution.JudgeWatch(*w, now.Sub(w.StartedAt))
	w.Status, w.Verdict = status, verdict
	if status == domain.EvoWatchHealthy || status == domain.EvoWatchEnded {
		w.EndedAt = now
	}
	after := *w
	after.CheckedAt = before.CheckedAt
	if after != before {
		w.CheckedAt = now
		if !save() {
			stored, _ := h.repo.EvoProposal(p.ID)
			return stored
		}
	}
	if status != domain.EvoWatchDegraded || !h.autoEvolution(asset) {
		return p
	}
	target, found := h.repo.AssetVersion(asset.Kind, asset.ID, w.PrevVersion)
	if !found {
		return p
	}
	if open, exists := h.repo.OpenEvoProposalForAsset(asset.Kind, asset.ID); exists {
		log.Printf("evolution watch %s: auto rollback skipped, proposal %s is open", p.ID, open.ID)
		return p
	}
	if _, err := h.rollbackTo(asset, target, 0, "L3 自动回滚："+verdict); err != nil {
		log.Printf("evolution watch %s auto rollback: %v", p.ID, err)
		return p
	}
	stored, _ := h.repo.EvoProposal(p.ID)
	return stored
}

// rollbackTo writes a stored version back into the live asset, appends a
// rollback version and closes released proposals (and their watches) newer
// than the target.
func (h *evolutionHandlers) rollbackTo(asset evoAsset, target domain.AssetVersion, userID int64, note string) (domain.AssetVersion, error) {
	if note = strings.TrimSpace(note); note == "" {
		note = "回滚到 v" + strconv.Itoa(target.Version)
	}
	return h.repo.RollbackEvoAsset(asset.Kind, asset.ID, domain.ContentHash(asset.Content), target.Version, userID, note)
}

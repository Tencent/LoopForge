package evolution

import (
	"fmt"
	"hash/fnv"
	"math"
	"time"

	"agentflow/internal/domain"
)

// Trial (canary) and post-release watch thresholds.
const (
	DefaultCanaryPercent = 50
	DefaultCanaryMinRuns = 5
	MinCanaryPercent     = 10
	MaxCanaryPercent     = 90
	MaxCanaryMinRuns     = 50

	// A canary that is clearly worse is stopped before it reaches MinRuns.
	CanaryEarlyStopRuns = 3
	CanaryEarlyStopProb = 0.95
	// At MinRuns per arm the candidate fails when it is probably worse.
	CanaryFailProb = 0.8

	DefaultWatchMinRuns = 5
	// A release is degraded when it is very probably worse than before.
	WatchDegradeProb   = 0.9
	WatchMinBaseline   = 3
	WatchBaselineRuns  = 30
	WatchBaselineDays  = 30
	WatchMaxAge        = 14 * 24 * time.Hour
	WatchStaleAge      = 30 * 24 * time.Hour
	TrialRunScanLimit  = 200
	watchHealthyFactor = 2
)

// CanaryBucket maps a run deterministically into [0,100) for one proposal,
// so a run's arm never changes and different trials split independently.
func CanaryBucket(runID, proposalID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(runID + "|" + proposalID))
	return int(h.Sum32() % 100)
}

// ClampCanary normalises trial settings.
func ClampCanary(percent, minRuns int) (int, int) {
	if percent <= 0 {
		percent = DefaultCanaryPercent
	}
	percent = min(max(percent, MinCanaryPercent), MaxCanaryPercent)
	if minRuns <= 0 {
		minRuns = DefaultCanaryMinRuns
	}
	minRuns = min(max(minRuns, CanaryEarlyStopRuns), MaxCanaryMinRuns)
	return percent, minRuns
}

// AssetRunScore grades how the nodes an asset shapes ended in one business
// run: 2 clean, 1 succeeded after rework, 0 failed. ok is false when the
// run is not terminal, was cancelled, or never reached those nodes, so it
// says nothing about the asset.
func AssetRunScore(run domain.Run, nodes map[string]bool) (int, bool) {
	if run.Status != "succeeded" && run.Status != "failed" {
		return 0, false
	}
	ran, reworked := false, false
	for id := range nodes {
		nr := run.Nodes[id]
		if nr == nil {
			continue
		}
		switch nr.Status {
		case "failed":
			return 0, true
		case "succeeded":
			ran = true
			if nr.ReworkCount > 0 {
				reworked = true
			}
		}
	}
	if !ran {
		return 0, false
	}
	if reworked {
		return 1, true
	}
	return 2, true
}

// AddScore folds one AssetRunScore into arm stats.
func AddScore(s *domain.EvoArmStats, score int) {
	s.Runs++
	switch score {
	case 2:
		s.Clean++
	case 1:
		s.Reworked++
	default:
		s.Failed++
	}
	s.Rate = float64(s.Clean) / float64(s.Runs)
}

func lbeta(a, b float64) float64 {
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	return la + lb - lab
}

// ProbWorse is the posterior probability that arm a's clean rate is below
// arm b's, with independent Beta(1+clean, 1+other) posteriors (uniform
// prior). Exact closed form for integer parameters (Evan Miller):
// P(pB > pA) = Σ_{i=0}^{αB-1} B(αA+i, βA+βB) / ((βB+i) B(1+i, βB) B(αA, βA)).
func ProbWorse(a, b domain.EvoArmStats) float64 {
	alphaA, betaA := float64(1+a.Clean), float64(1+a.Runs-a.Clean)
	alphaB, betaB := 1+b.Clean, float64(1+b.Runs-b.Clean)
	total := 0.0
	for i := 0; i < alphaB; i++ {
		fi := float64(i)
		total += math.Exp(lbeta(alphaA+fi, betaA+betaB) - math.Log(betaB+fi) - lbeta(1+fi, betaB) - lbeta(alphaA, betaA))
	}
	return math.Min(1, math.Max(0, total))
}

func pct(rate float64) string { return fmt.Sprintf("%.0f%%", rate*100) }

func armLine(name string, s domain.EvoArmStats) string {
	return fmt.Sprintf("%s %d 次（一次成功 %s）", name, s.Runs, pct(s.Rate))
}

// JudgeCanary decides a running trial: failed when the candidate is
// clearly or probably worse, passed when both arms reached MinRuns and the
// candidate is not probably worse, otherwise still running.
func JudgeCanary(c domain.EvoCanary) (string, string) {
	c.ProbWorse = ProbWorse(c.Canary, c.Control)
	stats := armLine("试用版", c.Canary) + "，" + armLine("当前版", c.Control) + fmt.Sprintf("，试用版更差的概率 %s", pct(c.ProbWorse))
	switch {
	case c.Canary.Runs >= CanaryEarlyStopRuns && c.Control.Runs >= CanaryEarlyStopRuns && c.ProbWorse >= CanaryEarlyStopProb:
		return domain.EvoCanaryFailed, "试用版明显更差，已提前停止：" + stats
	case c.Canary.Runs < c.MinRuns || c.Control.Runs < c.MinRuns:
		return domain.EvoCanaryRunning, fmt.Sprintf("试用中，每组需要 %d 次运行：", c.MinRuns) + stats
	case c.ProbWorse >= CanaryFailProb:
		return domain.EvoCanaryFailed, "试用版效果更差，已停止试用：" + stats
	}
	return domain.EvoCanaryPassed, "试用版不差于当前版：" + stats
}

// JudgeWatch decides a post-release watch after the released version ran
// on real runs: degraded when it is very probably worse than the version
// it replaced, healthy once enough runs show no degradation.
func JudgeWatch(w domain.EvoWatch, age time.Duration) (string, string) {
	w.ProbWorse = ProbWorse(w.Current, w.Baseline)
	stats := armLine(fmt.Sprintf("v%d", w.Version), w.Current) + "，" + armLine(fmt.Sprintf("发布前 v%d", w.PrevVersion), w.Baseline)
	if w.Baseline.Runs >= WatchMinBaseline {
		stats += fmt.Sprintf("，新版本更差的概率 %s", pct(w.ProbWorse))
	}
	switch {
	case w.Current.Runs < w.MinRuns:
		if age >= WatchStaleAge {
			return domain.EvoWatchEnded, "观察期内运行太少，停止观察：" + stats
		}
		return domain.EvoWatchWatching, fmt.Sprintf("观察中，需要 %d 次运行：", w.MinRuns) + stats
	case w.Baseline.Runs < WatchMinBaseline:
		if w.Current.Runs >= w.MinRuns*watchHealthyFactor || age >= WatchMaxAge {
			return domain.EvoWatchEnded, "发布前的版本样本不足，只记录不判定：" + stats
		}
		return domain.EvoWatchWatching, "发布前的版本样本不足，只记录不判定：" + stats
	case w.ProbWorse >= WatchDegradeProb:
		return domain.EvoWatchDegraded, "发布后效果变差：" + stats
	case w.Current.Runs >= w.MinRuns*watchHealthyFactor || age >= WatchMaxAge:
		return domain.EvoWatchHealthy, "发布后效果没有变差，结束观察：" + stats
	}
	return domain.EvoWatchWatching, "观察中，暂未发现变差：" + stats
}

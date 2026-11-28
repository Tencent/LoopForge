package evolution

import (
	"math"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func arm(clean, reworked, failed int) domain.EvoArmStats {
	s := domain.EvoArmStats{}
	for i := 0; i < clean; i++ {
		AddScore(&s, 2)
	}
	for i := 0; i < reworked; i++ {
		AddScore(&s, 1)
	}
	for i := 0; i < failed; i++ {
		AddScore(&s, 0)
	}
	return s
}

func TestProbWorse(t *testing.T) {
	if p := ProbWorse(arm(0, 0, 0), arm(0, 0, 0)); math.Abs(p-0.5) > 1e-9 {
		t.Fatalf("no data = %v, want 0.5", p)
	}
	if p := ProbWorse(arm(5, 0, 0), arm(5, 0, 0)); math.Abs(p-0.5) > 1e-9 {
		t.Fatalf("equal arms = %v, want 0.5", p)
	}
	// Beta(1,2) vs Beta(2,1): P(X<Y) = 5/6 for this pair.
	if p := ProbWorse(arm(0, 0, 1), arm(1, 0, 0)); math.Abs(p-5.0/6) > 1e-9 {
		t.Fatalf("one run each = %v, want 5/6", p)
	}
	worse, better := ProbWorse(arm(2, 1, 2), arm(5, 0, 0)), ProbWorse(arm(5, 0, 0), arm(2, 1, 2))
	if worse < 0.9 || math.Abs(worse+better-1) > 1e-9 {
		t.Fatalf("2/5 vs 5/5: worse=%v better=%v", worse, better)
	}
}

func TestAssetRunScore(t *testing.T) {
	nodes := map[string]bool{"code": true}
	cases := []struct {
		status string
		node   *domain.NodeRun
		score  int
		ok     bool
	}{
		{"succeeded", &domain.NodeRun{Status: "succeeded"}, 2, true},
		{"succeeded", &domain.NodeRun{Status: "succeeded", ReworkCount: 1}, 1, true},
		{"failed", &domain.NodeRun{Status: "failed"}, 0, true},
		{"failed", &domain.NodeRun{Status: "succeeded"}, 2, true},
		{"failed", &domain.NodeRun{Status: "pending"}, 0, false},
		{"cancelled", &domain.NodeRun{Status: "failed"}, 0, false},
		{"running", &domain.NodeRun{Status: "succeeded"}, 0, false},
	}
	for i, c := range cases {
		score, ok := AssetRunScore(domain.Run{Status: c.status, Nodes: map[string]*domain.NodeRun{"code": c.node}}, nodes)
		if score != c.score || ok != c.ok {
			t.Fatalf("case %d: got (%d,%v) want (%d,%v)", i, score, ok, c.score, c.ok)
		}
	}
}

func TestCanaryBucketAndClamp(t *testing.T) {
	if CanaryBucket("run-1", "evp-1") != CanaryBucket("run-1", "evp-1") {
		t.Fatal("bucket must be stable")
	}
	in := 0
	for i := 0; i < 2000; i++ {
		if CanaryBucket("run-"+time.Duration(i).String(), "evp-x") < 30 {
			in++
		}
	}
	if in < 450 || in > 750 {
		t.Fatalf("30%% bucket took %d of 2000", in)
	}
	if p, n := ClampCanary(0, 0); p != DefaultCanaryPercent || n != DefaultCanaryMinRuns {
		t.Fatalf("defaults = %d,%d", p, n)
	}
	if p, n := ClampCanary(100, 1); p != MaxCanaryPercent || n != CanaryEarlyStopRuns {
		t.Fatalf("clamped = %d,%d", p, n)
	}
}

func TestJudgeCanary(t *testing.T) {
	c := domain.EvoCanary{MinRuns: 5, Canary: arm(2, 0, 0), Control: arm(3, 0, 0)}
	if status, _ := JudgeCanary(c); status != domain.EvoCanaryRunning {
		t.Fatalf("too few runs should keep running: %s", status)
	}
	c.Canary, c.Control = arm(0, 0, 3), arm(3, 0, 0)
	if status, verdict := JudgeCanary(c); status != domain.EvoCanaryFailed || !strings.Contains(verdict, "提前停止") {
		t.Fatalf("clearly worse should stop early: %s %s", status, verdict)
	}
	c.Canary, c.Control = arm(5, 1, 0), arm(5, 0, 1)
	if status, verdict := JudgeCanary(c); status != domain.EvoCanaryPassed || !strings.Contains(verdict, "不差于当前版") {
		t.Fatalf("comparable arms should pass: %s %s", status, verdict)
	}
	c.Canary, c.Control = arm(3, 2, 1), arm(6, 0, 0)
	if status, _ := JudgeCanary(c); status != domain.EvoCanaryFailed {
		t.Fatalf("probably worse at MinRuns should fail: %s (p=%v)", status, ProbWorse(c.Canary, c.Control))
	}
}

func TestJudgeWatch(t *testing.T) {
	w := domain.EvoWatch{Version: 3, PrevVersion: 2, MinRuns: 5, Baseline: arm(9, 1, 0), Current: arm(2, 0, 0)}
	if status, _ := JudgeWatch(w, time.Hour); status != domain.EvoWatchWatching {
		t.Fatalf("too few runs keep watching: %s", status)
	}
	if status, _ := JudgeWatch(w, WatchStaleAge); status != domain.EvoWatchEnded {
		t.Fatalf("a stale watch ends: %s", status)
	}
	w.Current = arm(2, 0, 3)
	if status, verdict := JudgeWatch(w, time.Hour); status != domain.EvoWatchDegraded || !strings.Contains(verdict, "变差") {
		t.Fatalf("worse release is degraded: %s %s", status, verdict)
	}
	w.Current = arm(9, 1, 0)
	if status, _ := JudgeWatch(w, time.Hour); status != domain.EvoWatchHealthy {
		t.Fatalf("enough good runs end healthy: %s", status)
	}
	w.Baseline, w.Current = arm(1, 0, 0), arm(0, 0, 5)
	if status, verdict := JudgeWatch(w, time.Hour); status != domain.EvoWatchWatching || !strings.Contains(verdict, "样本不足") {
		t.Fatalf("no baseline means no verdict: %s %s", status, verdict)
	}
}

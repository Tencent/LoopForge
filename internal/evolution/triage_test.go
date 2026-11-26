package evolution

import (
	"math"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestBetaCDFAndQuantile(t *testing.T) {
	cases := []struct{ x, a, b, want float64 }{
		{0.3, 1, 1, 0.3},
		{0.5, 2, 3, 0.6875},
	}
	for _, c := range cases {
		if got := betaCDF(c.x, c.a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("I_%v(%v,%v)=%v want %v", c.x, c.a, c.b, got, c.want)
		}
	}
	for _, p := range []float64{0.05, 0.5, 0.95} {
		q := betaQuantile(p, 9.8, 42.2)
		if got := betaCDF(q, 9.8, 42.2); math.Abs(got-p) > 1e-6 {
			t.Fatalf("quantile(%v) round trip gave %v", p, got)
		}
	}
	if betaCDF(0, 2, 2) != 0 || betaCDF(1, 2, 2) != 1 {
		t.Fatal("bounds")
	}
}

func TestEvalTriageSeparatesCostlyFromNoisy(t *testing.T) {
	p0 := 0.08
	stat := func(runs int, problems float64) triageStats {
		return triageStats{n: float64(runs), k: problems, runs: runs, problem: problems, windowDays: 14}
	}
	bad := evalTriage(stat(42, 9), p0)
	if bad.Verdict != domain.EvoVerdictFix || bad.ProbWorse < 0.95 || bad.WeeklyExcess < 2 || bad.RateLow > bad.Rate || bad.RateHigh < bad.Rate {
		t.Fatalf("42 runs / 9 problems should be worth fixing: %+v", bad)
	}
	nearBaseline := evalTriage(stat(100, 10), p0)
	if nearBaseline.Verdict == domain.EvoVerdictFix {
		t.Fatalf("10%% vs 8%% baseline is not conclusive: %+v", nearBaseline)
	}
	rare := evalTriage(stat(3, 3), p0)
	if rare.Verdict != domain.EvoVerdictWatch || rare.ProbWorse < TriageFixProb || rare.WeeklyExcess >= TriageMinWeeklyExcess {
		t.Fatalf("3/3 failures on a rarely used asset is likely bad but cheap: %+v", rare)
	}
	fine := evalTriage(stat(50, 3), p0)
	if fine.Verdict != domain.EvoVerdictNoise {
		t.Fatalf("6%% vs 8%% baseline is noise: %+v", fine)
	}
	unsure := evalTriage(stat(8, 2), p0)
	if unsure.Verdict != domain.EvoVerdictWatch || unsure.RunsToDecide <= 0 {
		t.Fatalf("2/8 should ask for more runs: %+v", unsure)
	}
	if bad.Score <= rare.Score {
		t.Fatalf("costly asset must rank above the rare one: %v <= %v", bad.Score, rare.Score)
	}
}

func TestDiagnoseTriageUsesExposureVersionsAndDecay(t *testing.T) {
	now := time.Now().UTC()
	runs := []domain.Run{}
	for i := 0; i < 10; i++ {
		id := "ok-" + string(rune('a'+i))
		runs = append(runs, testRun(id, "succeeded", map[string]*domain.NodeRun{"plan": {Status: "succeeded"}, "code": {Status: "succeeded"}}))
	}
	failed := func(id string, at time.Time) domain.Run {
		r := testRun(id, "failed", map[string]*domain.NodeRun{"plan": {Status: "succeeded"}, "code": {Status: "failed", Error: "编译失败 第 12 行"}})
		r.UpdatedAt = at
		return r
	}
	runs = append(runs, failed("f-1", now), failed("f-2", now), failed("f-old", now.AddDate(0, 0, -13)), failed("f-stale", now))
	in := testInput(runs)
	in.Now, in.WindowDays = now, 14
	in.Pins = func(runID string) []domain.RunAssetPin {
		if runID == "f-stale" {
			return []domain.RunAssetPin{{RunID: runID, AssetKind: domain.EvoAssetAgent, AssetID: "coder", Hash: "older-body"}}
		}
		return nil
	}
	found := Diagnose(in)
	if len(found) != 1 || found[0].AssetID != "coder" {
		t.Fatalf("want one coder finding, got %+v", found)
	}
	f := found[0]
	tr := f.Triage
	if tr == nil || tr.Runs != 13 || tr.StaleRuns != 1 || tr.ProblemRuns != 3 || tr.Kinds[SignalNodeFailed] != 3 {
		t.Fatalf("exposure should count current-version runs only: %+v", tr)
	}
	for _, id := range f.SampleRunIDs {
		if id == "f-stale" {
			t.Fatal("stale-version run must not be a sample")
		}
	}
	if tr.BaselineRate <= 0 || tr.Rate <= tr.BaselineRate || tr.ProbWorse <= 0.5 {
		t.Fatalf("coder should look worse than the baseline: %+v", tr)
	}
	if len(f.Trend) != 14 || f.Trend[13] != 2 || f.Trend[0] != 1 {
		t.Fatalf("trend buckets by day: %v", f.Trend)
	}
	if len(f.Clusters) != 1 || f.Clusters[0].Runs != 3 || f.Clusters[0].Label != "编译失败 第 # 行" {
		t.Fatalf("same error with different numbers should cluster: %+v", f.Clusters)
	}
	if f.FirstSeenAt.IsZero() || !f.LastSeenAt.After(f.FirstSeenAt) {
		t.Fatalf("first/last seen: %v %v", f.FirstSeenAt, f.LastSeenAt)
	}
	if f.Severity != verdictSeverity(tr.Verdict) {
		t.Fatalf("severity mirrors verdict: %s vs %s", f.Severity, tr.Verdict)
	}
}

func TestClustersKeepAllClassesAndRuns(t *testing.T) {
	now := time.Now().UTC()
	signals := []domain.EvoSignal{}
	for i := 0; i < 40; i++ {
		signals = append(signals, domain.EvoSignal{RunID: string(rune('A' + i)), Kind: SignalNodeFailed, NodeID: "code", Detail: "compile failed line 12", CreatedAt: now})
	}
	signals = append(signals, signals[0])
	for i := 0; i < 8; i++ {
		signals = append(signals, domain.EvoSignal{RunID: "other", Kind: SignalNodeFailed, NodeID: string(rune('a' + i)), Detail: "different", CreatedAt: now.Add(-24 * time.Hour)})
	}
	clusters := buildClusters(signals, now, 14)
	if len(clusters) != 9 {
		t.Fatalf("lost classes: %d", len(clusters))
	}
	c := clusters[0]
	if c.Count != 41 || c.Runs != 40 || len(c.Occurrences) != 40 || c.Trend[13] != 40 || c.ID == "" {
		t.Fatalf("cluster lost or double-counted runs: %+v", c)
	}
	if c.Occurrences[0].Count != 2 || c.RunID != c.Occurrences[0].RunID {
		t.Fatalf("incorrect duplicate aggregation: %+v", c)
	}
	for i, j := 0, len(signals)-1; i < j; i, j = i+1, j-1 {
		signals[i], signals[j] = signals[j], signals[i]
	}
	again := buildClusters(signals, now, 14)
	for i := range clusters {
		if clusters[i].ID != again[i].ID || clusters[i].RunID != again[i].RunID {
			t.Fatal("unstable grouping")
		}
	}
}

func TestDecayWeight(t *testing.T) {
	now := time.Now()
	if w := decayWeight(now, now.Add(-TriageHalfLife)); math.Abs(w-0.5) > 1e-9 {
		t.Fatalf("half life: %v", w)
	}
	if decayWeight(now, time.Time{}) != 1 || decayWeight(now, now.Add(time.Hour)) != 1 {
		t.Fatal("zero or future time weighs 1")
	}
}

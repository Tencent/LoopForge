package engine

import (
	"math"
	"testing"

	"agentflow/internal/domain"
)

func TestApplyUsageDoesNotRecountOlderOrPartialSnapshots(t *testing.T) {
	t.Parallel()
	run := domain.Run{}
	nr := domain.NodeRun{}
	first := domain.TokenUsage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CacheReadTokens: 80, CacheWriteTokens: 20, ReasoningTokens: 5}
	applyUsage(&run, &nr, "s1", first)
	// Each token counter has its own cumulative maximum. Some providers omit
	// counters in intermediate updates, so a smaller field cannot reset it.
	applyUsage(&run, &nr, "s1", domain.TokenUsage{InputTokens: 80, OutputTokens: 12, TotalTokens: 92, CacheReadTokens: 70, CacheWriteTokens: 10, ReasoningTokens: 4})
	applyUsage(&run, &nr, "s1", domain.TokenUsage{CostStatus: "reported"})
	want := domain.TokenUsage{InputTokens: 120, OutputTokens: 15, TotalTokens: 135, CacheReadTokens: 90, CacheWriteTokens: 25, ReasoningTokens: 6, CostStatus: "reported"}
	applyUsage(&run, &nr, "s1", want)
	for label, got := range map[string]domain.TokenUsage{"run": run.Usage, "node": nr.Usage, "cursor": nr.UsageCursor} {
		if got != want {
			t.Errorf("%s usage=%+v, want %+v", label, got, want)
		}
	}
}

func TestApplyUsagePreservesCostCorrectionsAndResetsTokenCursorForNewSessions(t *testing.T) {
	t.Parallel()
	run := domain.Run{}
	nr := domain.NodeRun{SessionID: "s1"}
	applyUsage(&run, &nr, "", domain.TokenUsage{InputTokens: 100, CostUSD: 0.75, CostStatus: "reported"})
	applyUsage(&run, &nr, "", domain.TokenUsage{InputTokens: 80, CostUSD: 0.50, CostStatus: "estimated"})
	if math.Abs(run.Usage.CostUSD-0.50) > 1e-9 || run.Usage.CostStatus != "estimated" || nr.Usage.InputTokens != 100 {
		t.Fatalf("cost correction or token maximum lost: run=%+v node=%+v", run.Usage, nr.Usage)
	}
	applyUsage(&run, &nr, "s1", domain.TokenUsage{InputTokens: 120, CostUSD: 0.90, CostStatus: "reported"})
	nr.SessionID = "s2"
	applyUsage(&run, &nr, "", domain.TokenUsage{InputTokens: 50, CostUSD: 0.20, CostStatus: "reported"})
	applyUsage(&run, &nr, "", domain.TokenUsage{InputTokens: 10, CostUSD: 0.20})
	applyUsage(&run, &nr, "", domain.TokenUsage{InputTokens: 75, CostUSD: 0.30, CostStatus: "reported"})
	if run.Usage.InputTokens != 195 || nr.Usage.InputTokens != 195 || nr.UsageCursor.InputTokens != 75 || nr.UsageSessionID != "s2" || math.Abs(run.Usage.CostUSD-1.20) > 1e-9 {
		t.Fatalf("new session did not have an independent cursor: run=%+v node=%+v", run.Usage, nr)
	}
}

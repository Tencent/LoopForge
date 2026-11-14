package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestAccumulateBreakdownUsageAddsTokensAndCostUnknown(t *testing.T) {
	usage := domain.TokenUsage{
		TotalTokens:      10,
		InputTokens:      4,
		OutputTokens:     6,
		CacheReadTokens:  2,
		CacheWriteTokens: 1,
		CostUSD:          0.5,
		CostStatus:       "reported",
	}
	value := &analyticsAccumulator{}
	accumulateBreakdownUsage(value, usage)
	accumulateBreakdownUsage(value, usage)

	if value.item.Tokens != 20 || value.item.CostUSD != 1 {
		t.Fatalf("unexpected totals: tokens=%d cost=%v", value.item.Tokens, value.item.CostUSD)
	}
	if value.inputTokens != 8 || value.outputTokens != 12 || value.cacheReadTokens != 4 || value.cacheWriteTokens != 2 {
		t.Fatalf("unexpected token split: %+v", value)
	}
	if value.item.CostUnknownRuns != 0 {
		t.Fatalf("expected no unknown-cost runs, got %d", value.item.CostUnknownRuns)
	}

	accumulateBreakdownUsage(value, domain.TokenUsage{CostStatus: "unknown"})
	if value.item.CostUnknownRuns != 1 {
		t.Fatalf("expected 1 unknown-cost run, got %d", value.item.CostUnknownRuns)
	}
}

func TestCountBreakdownStatusCountsTerminalStates(t *testing.T) {
	cases := []struct {
		status   string
		active   bool
		want     domain.AnalyticsBreakdown
		terminal string
	}{
		{status: "succeeded", want: domain.AnalyticsBreakdown{Succeeded: 1}, terminal: "succeeded"},
		{status: "failed", want: domain.AnalyticsBreakdown{Failed: 1}, terminal: "failed"},
		{status: "cancelled", want: domain.AnalyticsBreakdown{Cancelled: 1}, terminal: "cancelled"},
		{status: "running", active: true, want: domain.AnalyticsBreakdown{Active: 1}, terminal: "running"},
		{status: "waiting", active: true, want: domain.AnalyticsBreakdown{Active: 1}, terminal: "waiting"},
		{status: "skipped", active: false, want: domain.AnalyticsBreakdown{}, terminal: "skipped"},
	}
	for _, c := range cases {
		t.Run(c.status, func(t *testing.T) {
			var item domain.AnalyticsBreakdown
			countBreakdownStatus(&item, c.status, c.active)
			if item.Succeeded != c.want.Succeeded || item.Failed != c.want.Failed ||
				item.Cancelled != c.want.Cancelled || item.Active != c.want.Active {
				t.Fatalf("countBreakdownStatus(%q) = succeeded:%d failed:%d cancelled:%d active:%d, want %+v",
					c.status, item.Succeeded, item.Failed, item.Cancelled, item.Active, c.want)
			}
		})
	}
}

func TestAccumulateBreakdownDurationKeepsSamplesInOrder(t *testing.T) {
	value := &analyticsAccumulator{}
	accumulateBreakdownDuration(value, 100, 40)
	accumulateBreakdownDuration(value, 300, 60)

	if value.duration != 400 || value.execDuration != 100 {
		t.Fatalf("unexpected totals: duration=%d execDuration=%d", value.duration, value.execDuration)
	}
	if len(value.durations) != 2 || value.durations[0] != 100 || value.durations[1] != 300 {
		t.Fatalf("unexpected duration samples: %v", value.durations)
	}
	if len(value.execDurations) != 2 || value.execDurations[0] != 40 || value.execDurations[1] != 60 {
		t.Fatalf("unexpected exec duration samples: %v", value.execDurations)
	}
}

func TestAccumulateNodeStillCountsFailedNodes(t *testing.T) {
	values := map[string]*analyticsAccumulator{}
	accumulateNode(values, "n1", "节点一", &domain.NodeRun{Status: "failed", Attempt: 2, ReworkCount: 1}, 120, true)

	value := values["n1"]
	if value == nil {
		t.Fatal("expected an accumulator for n1")
	}
	if value.item.Runs != 1 || value.item.Failed != 1 || value.item.FailedNodes != 1 {
		t.Fatalf("unexpected status counters: %+v", value.item)
	}
	if value.retriedNodes != 1 || value.reworkNodes != 1 || value.nodeRuns != 1 {
		t.Fatalf("unexpected node counters: %+v", value)
	}
	if value.item.AverageDurationMs != 0 && value.duration != 120 {
		t.Fatalf("expected duration 120, got %d", value.duration)
	}
}

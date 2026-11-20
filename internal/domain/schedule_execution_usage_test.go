package domain

import (
	"math"
	"testing"
)

func TestScheduleUsagePricesRoutedModels(t *testing.T) {
	for _, tc := range []struct {
		model                string
		input, output, cache int64
		wantCNY              float64
	}{
		{model: "codex:gpt-5.5", input: 100000, output: 10000, cache: 40000, wantCNY: 4.34},
		{model: " CODEX:gpt-5.5 ", input: 100000, output: 10000, cache: 40000, wantCNY: 4.34},
		{model: "gpt-5.5", input: 100000, output: 10000, cache: 40000, wantCNY: 4.34},
		{model: "claude-code:claude-sonnet-4-6", input: 100000, output: 10000, cache: 40000, wantCNY: 3.234},
		{model: "claude-code:gpt-5.5", input: 100000, output: 10000, cache: 40000, wantCNY: 5.74},
	} {
		t.Run(tc.model, func(t *testing.T) {
			u := TokenUsage{InputTokens: tc.input, OutputTokens: tc.output, CacheReadTokens: tc.cache, TotalTokens: tc.input + tc.output}
			e := ScheduleJobExecution{}
			e.RecordUsage(tc.model, &u)
			if e.Usage == nil || e.Usage.CostStatus != "estimated" || e.CostCNY == nil || math.Abs(*e.CostCNY-tc.wantCNY) > 1e-9 {
				t.Fatalf("usage=%+v cost=%v want=%f", e.Usage, e.CostCNY, tc.wantCNY)
			}
			if u.CostStatus != "" || u.CostUSD != 0 {
				t.Fatalf("source mutated: %+v", u)
			}
		})
	}
}

func TestScheduleUsageKeepsCacheOnlyReports(t *testing.T) {
	for _, u := range []TokenUsage{{CacheReadTokens: 40000}, {CacheWriteTokens: 40000}, {ReasoningTokens: 10}, {TotalTokens: 10}} {
		e := ScheduleJobExecution{}
		e.RecordUsage("claude-code:claude-sonnet-4-6", &u)
		if e.Usage == nil || e.Usage.CacheReadTokens != u.CacheReadTokens || e.Usage.CacheWriteTokens != u.CacheWriteTokens {
			t.Fatalf("usage lost: %+v", e)
		}
	}
	for _, u := range []*TokenUsage{nil, {}} {
		e := ScheduleJobExecution{}
		e.RecordUsage("codex:gpt-5.5", u)
		if e.Usage != nil {
			t.Fatalf("unexpected usage=%+v", e.Usage)
		}
	}
}

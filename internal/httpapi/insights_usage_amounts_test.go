package httpapi

import (
	"math"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestValidateInsightUsageAmounts(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	valid := domain.InsightUsage{Provider: "p", Model: "m", InputTokens: 100, OutputTokens: 20, CacheReadTokens: 60, CacheWriteTokens: 40, ReasoningTokens: 20, CostStatus: "reported", CostUSD: f(0)}
	const (
		tokenRange = "Token 数须在 0–10^12 之间"
		inclusion  = "缓存 Token 必须包含在输入中，推理 Token 必须包含在输出中"
		unknownSet = "未知成本必须为 null"
		knownCost  = "已知成本须为 0–10^9 USD"
		badStatus  = "costStatus 必须为 reported、estimated 或 unknown"
	)
	cases := []struct {
		name   string
		mutate func(*domain.InsightUsage)
		want   string
	}{
		{"valid boundaries", func(u *domain.InsightUsage) {}, ""},
		{"max tokens", func(u *domain.InsightUsage) { u.InputTokens, u.OutputTokens = 1e12, 1e12 }, ""},
		{"estimated max cost", func(u *domain.InsightUsage) { u.CostStatus, u.CostUSD = "estimated", f(1e9) }, ""},
		{"unknown nil cost", func(u *domain.InsightUsage) { u.CostStatus, u.CostUSD = "unknown", nil }, ""},
		{"negative input", func(u *domain.InsightUsage) { u.InputTokens = -1 }, tokenRange},
		{"too many output", func(u *domain.InsightUsage) { u.OutputTokens = 1e12 + 1 }, tokenRange},
		{"negative reasoning", func(u *domain.InsightUsage) { u.ReasoningTokens = -1 }, tokenRange},
		{"cache exceeds input", func(u *domain.InsightUsage) { u.CacheWriteTokens = 41 }, inclusion},
		{"reasoning exceeds output", func(u *domain.InsightUsage) { u.ReasoningTokens = 21 }, inclusion},
		{"unknown with cost", func(u *domain.InsightUsage) { u.CostStatus = "unknown" }, unknownSet},
		{"reported nil cost", func(u *domain.InsightUsage) { u.CostUSD = nil }, knownCost},
		{"negative cost", func(u *domain.InsightUsage) { u.CostUSD = f(-0.01) }, knownCost},
		{"cost too large", func(u *domain.InsightUsage) { u.CostUSD = f(1e9 + 1) }, knownCost},
		{"NaN cost", func(u *domain.InsightUsage) { u.CostUSD = f(math.NaN()) }, knownCost},
		{"Inf cost", func(u *domain.InsightUsage) { u.CostStatus, u.CostUSD = "estimated", f(math.Inf(1)) }, knownCost},
		{"bad status", func(u *domain.InsightUsage) { u.CostStatus = "free" }, badStatus},
		{"token range checked first", func(u *domain.InsightUsage) { u.InputTokens, u.CostStatus = -1, "free" }, tokenRange},
	}
	for _, tc := range cases {
		u := valid
		tc.mutate(&u)
		if got := validateInsightUsageAmounts(u); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestValidateInsightExecutionDuplicateBeforeAmounts(t *testing.T) {
	cost := 1.0
	finished := time.Now().UTC()
	v := domain.InsightExecution{
		ID: "id", Type: "workflow_execution", ExternalExecutionID: "external",
		Workflow:  domain.InsightWorkflow{ID: "wf", Name: "Workflow"},
		StartedAt: finished.Add(-time.Minute), FinishedAt: finished, Status: "succeeded",
		Usage: []domain.InsightUsage{{Provider: "p", Model: "m", InputTokens: 10, OutputTokens: 5, CostStatus: "reported", CostUSD: &cost}},
	}
	bad := v.Usage[0]
	bad.InputTokens = -1
	v.Usage = append(v.Usage, bad)
	if got := validateInsightExecution(&v, v.FinishedAt); got != "同一 provider/model 请合并为一项" {
		t.Fatalf("duplicate entry with bad amounts should report duplicate first, got %q", got)
	}
	v.Usage = v.Usage[:1]
	v.Usage[0].CostStatus = "free"
	if got := validateInsightExecution(&v, v.FinishedAt); got != "costStatus 必须为 reported、estimated 或 unknown" {
		t.Fatalf("amount errors should still surface, got %q", got)
	}
}

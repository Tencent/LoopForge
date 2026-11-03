package domain

import (
	"math"
	"testing"
)

func TestAnalyticsTokenPricing(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		usage       TokenUsage
		inclusive   bool
		cny         float64
		known       bool
	}{
		{"Claude native excludes caches", "claude-opus-4.6-1m", TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, CacheReadTokens: 1e6, CacheWriteTokens: 1e6}, false, (5 + 25 + .5 + 6.25) * 7, true},
		{"inclusive input does not double charge cache", "claude-sonnet-4-6", TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, CacheReadTokens: 5e5}, true, (1.5 + .15 + 15) * 7, true},
		{"CNY public price ignores cheap adapter amount", "glm-5.3-flash-ioa", TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, CostUSD: .001, CostStatus: "reported"}, false, 3.6, true},
		{"DeepSeek RMB input includes cache in OpenAI shape", "deepseek-v4-pro-ioa", TokenUsage{InputTokens: 1e6, CacheReadTokens: 5e5, OutputTokens: 1e6}, true, 4.5 + .15 + 27, true},
		{"USD public price uses fixed seven", "gpt-5.3-codex", TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, CacheReadTokens: 5e5, ReasoningTokens: 5e5}, true, (.875 + .0875 + 14) * 7, true},
		{"HY keeps reported tiny price", "hy-2.0", TokenUsage{InputTokens: 1e6, CostUSD: .001, CostStatus: "reported"}, false, .007, true},
		{"HY explicit free", "hy-2.0", TokenUsage{InputTokens: 1e6, CostStatus: "reported"}, false, 0, true},
		{"HY unknown is not free", "hy-2.0", TokenUsage{InputTokens: 1e6}, false, 0, false},
		{"unknown model is not cheap reported rate", "unknown-model", TokenUsage{InputTokens: 1e6, CostUSD: .001, CostStatus: "reported"}, false, 0, false},
		{"total alone cannot determine input output", "glm-5.3", TokenUsage{TotalTokens: 1e6}, false, 0, false},
		{"invalid inclusive cache", "gpt-5.4", TokenUsage{InputTokens: 10, CacheReadTokens: 11}, true, 0, false},
		{"no model call", "", TokenUsage{}, false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyticsPricedUsage(tc.model, tc.usage, tc.inclusive)
			if math.Abs(got.CostUSD*AnalyticsUSDToCNY-tc.cny) > 1e-8 || (got.CostStatus != "unknown") != tc.known {
				t.Fatalf("priced=%+v CNY=%g; want %g known=%v", got, got.CostUSD*AnalyticsUSDToCNY, tc.cny, tc.known)
			}
		})
	}
}

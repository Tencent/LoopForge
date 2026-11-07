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
		{"HY is free even with reported amount", "hy-2.0", TokenUsage{InputTokens: 1e6, CostUSD: .001, CostStatus: "reported"}, false, 0, true},
		{"HY explicit free", "hy-2.0", TokenUsage{InputTokens: 1e6, CostStatus: "reported"}, false, 0, true},
		{"HY without reported amount is free", "hy-2.0", TokenUsage{InputTokens: 1e6}, false, 0, true},
		{"unknown model is not cheap reported rate", "unknown-model", TokenUsage{InputTokens: 1e6, CostUSD: .001, CostStatus: "reported"}, false, 0, false},
		{"total alone cannot determine input output", "glm-5.3", TokenUsage{TotalTokens: 1e6}, false, 0, false},
		{"invalid inclusive cache", "gpt-5.4", TokenUsage{InputTokens: 10, CacheReadTokens: 11}, true, 0, false},
		{"Kimi K3.1 Pro TokenHub RMB price", "kimi-k3.1-pro", TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, CacheReadTokens: 1e6, CacheWriteTokens: 1e6}, false, 13 + 68 + 1.3 + 13, true},
		{"MiMo V2.6 Pro RMB price", "mimo-v2.6-pro", TokenUsage{InputTokens: 1e6, OutputTokens: 1e6, CacheReadTokens: 1e6}, false, 3 + 6 + .025, true},
		{"custom-local uses vendor public price", "custom-local:deepseek-v4-flash", TokenUsage{InputTokens: 1e6, OutputTokens: 1e6}, false, 2 + 8, true},
		{"CodeBuddy Hy3 is free", "hy3-ioa", TokenUsage{InputTokens: 1e6}, false, 0, true},
		{"CodeBuddy Hy4 is free", "hy4std", TokenUsage{InputTokens: 1e6, OutputTokens: 1e6}, false, 0, true},
		{"Hy4 preview ignores reported amount", "hy4-preview-ioa", TokenUsage{InputTokens: 1e6, CostUSD: .001, CostStatus: "reported"}, false, 0, true},
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

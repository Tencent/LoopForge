package domain

import (
	"math"
	"testing"
)

// A recorded "cli:model" ID must price by the published tariff of its model,
// and only the CLI decides whether the input figure already holds the caches.
func TestPricedRecordedModelUsageResolvesCLIAndTariff(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		usage       TokenUsage
		cny         float64
		known       bool
	}{
		// Codex: inclusive input, so 100k input - 40k cache read = 60k charged
		// at $5/M, 40k read at $0.5/M and 10k output at $30/M.
		{"codex prefix", "codex:gpt-5.5", TokenUsage{InputTokens: 100000, OutputTokens: 10000, CacheReadTokens: 40000}, (0.3 + 0.02 + 0.3) * 7, true},
		{"codex prefix is case-insensitive", " Codex:GPT-5.5 ", TokenUsage{InputTokens: 100000, OutputTokens: 10000, CacheReadTokens: 40000}, (0.3 + 0.02 + 0.3) * 7, true},
		// Claude Code reports input without caches, so all 1M input and the
		// 500k reads are charged separately.
		{"claude prefix", "claude-code:claude-sonnet-4-6", TokenUsage{InputTokens: 1000000, OutputTokens: 1000000, CacheReadTokens: 500000}, (3 + 15 + 0.15) * 7, true},
		// A Codex gateway may name an Anthropic model; the CLI still decides.
		{"codex on a claude model", "codex:claude-sonnet-4-6", TokenUsage{InputTokens: 1000000, CacheReadTokens: 400000, OutputTokens: 1000}, (1.8 + 0.12 + 0.015) * 7, true},
		// A conversation without a CLI prefix keeps the GPT family rule.
		{"unprefixed gpt", "gpt-5.5", TokenUsage{InputTokens: 100000, OutputTokens: 10000, CacheReadTokens: 40000}, (0.3 + 0.02 + 0.3) * 7, true},
		{"unprefixed claude", "claude-sonnet-4-6", TokenUsage{InputTokens: 1000000, OutputTokens: 1000000, CacheReadTokens: 500000}, (3 + 15 + 0.15) * 7, true},
		// A vendor-prefixed ID is not CLI routing and stays unknown, as before.
		{"vendor prefix is not a CLI", "openai:gpt-5.5", TokenUsage{InputTokens: 1000, OutputTokens: 1000}, 0, false},
		{"unknown CLI model", "codex:not-a-model", TokenUsage{InputTokens: 1000, OutputTokens: 1000}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PricedRecordedModelUsage(tc.model, tc.usage)
			if (got.CostStatus == "unknown") == tc.known {
				t.Fatalf("priced = %+v, want known=%v", got, tc.known)
			}
			if tc.known && math.Abs(got.CostUSD*AnalyticsUSDToCNY-tc.cny) > 1e-9 {
				t.Fatalf("CNY = %v, want %v", got.CostUSD*AnalyticsUSDToCNY, tc.cny)
			}
		})
	}
}

// A bare vendor ID has no CLI to route on, so its prefix is model text.
func TestRecordedModelEngineOnlySplitsKnownCLIs(t *testing.T) {
	for _, tc := range []struct{ model, engine, id string }{
		{"codex:gpt-5.5", "codex", "gpt-5.5"},
		{"claude-code:claude-sonnet-4-6", "claude-code", "claude-sonnet-4-6"},
		{"codebuddy:deepseek-v4.1-flash", "codebuddy", "deepseek-v4.1-flash"},
		{"dsh:deepseek-v4.1-flash", "dsh", "deepseek-v4.1-flash"},
		{"gpt-5.5", "", "gpt-5.5"},
		{"openai:gpt-5.5", "", "openai:gpt-5.5"},
		{"custom-local:deepseek-v4-flash", "", "custom-local:deepseek-v4-flash"},
	} {
		if engine, id := recordedModelEngine(tc.model); engine != tc.engine || id != tc.id {
			t.Errorf("recordedModelEngine(%q) = %q, %q; want %q, %q", tc.model, engine, id, tc.engine, tc.id)
		}
	}
}

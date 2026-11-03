package domain

import (
	"math"
	"regexp"
	"strings"
)

const AnalyticsUSDToCNY = 7.0
const AnalyticsPricingVersion = "public-standard-2026-11-02"

// TokenPrice is the published standard short-context price per million tokens.
// Currency identifies the native tariff; RMB rates are used directly rather
// than derived from dollar tariffs. Analytics keeps USD equivalents for compatibility.
// Sources and the limits of aggregate-token estimates are in docs/token-pricing.md.
type TokenPrice struct {
	Currency                             string
	Input, Output, CacheRead, CacheWrite float64
}

var publicTokenPrices = map[string]TokenPrice{
	"gpt-5.5":          {"USD", 5, 30, .5, 5},
	"claude-haiku-5-5": {"USD", .1, .5, .01, .125},
	// OpenAI official API pricing / model pages, checked 2026-11-02.
	"gpt-6-astra":   {"USD", 10, 50, 1, 12.5},
	"gpt-6.1-sol":   {"USD", 2, 10, .1, 2.5},
	"gpt-6.1-terra": {"USD", 1, 5, .05, 1.25},
	"gpt-6-sol":     {"USD", 2, 10, .2, 2.5},
	"gpt-6-luna":    {"USD", .1, .5, .01, .125},
	"gpt-5.6-sol":   {"USD", 4, 20, .4, 5},
	"gpt-5.6-terra": {"USD", 2, 12, .2, 2.5},
	"gpt-5.6-luna":  {"USD", .2, 1.2, .02, .25},
	"gpt-5.4":       {"USD", 2.5, 15, .25, 2.5},
	"gpt-5.3-codex": {"USD", 1.75, 14, .175, 1.75},
	"gpt-5.2":       {"USD", 1.75, 14, .175, 1.75},
	// Anthropic first-party standard API, five-minute cache creation.
	"claude-fable-5-1":  {"USD", 10, 50, .25, 12.5},
	"claude-fable-5":    {"USD", 10, 50, 1, 12.5},
	"claude-opus-5-5":   {"USD", 4, 20, .2, 5},
	"claude-opus-5":     {"USD", 5, 25, .5, 6.25},
	"claude-opus-4-8":   {"USD", 5, 25, .5, 6.25},
	"claude-opus-4-7":   {"USD", 5, 25, .5, 6.25},
	"claude-opus-4-6":   {"USD", 5, 25, .5, 6.25},
	"claude-opus-4-5":   {"USD", 5, 25, .5, 6.25},
	"claude-opus-4-1":   {"USD", 15, 75, 1.5, 18.75},
	"claude-opus-4":     {"USD", 15, 75, 1.5, 18.75},
	"claude-sonnet-5-5": {"USD", 2, 10, .1, 2.5},
	"claude-sonnet-5":   {"USD", 2, 10, .2, 2.5},
	"claude-sonnet-4-6": {"USD", 3, 15, .3, 3.75},
	"claude-sonnet-4-5": {"USD", 3, 15, .3, 3.75},
	"claude-sonnet-4":   {"USD", 3, 15, .3, 3.75},
	"claude-haiku-4-5":  {"USD", 1, 5, .1, 1.25},
	"claude-haiku-3-5":  {"USD", .8, 4, .08, 1},
	// Native RMB public prices: DeepSeek, BigModel, and TokenHub domestic pay-as-you-go.
	// DeepSeek uses the standard peak rate; aggregate data cannot reconstruct peak/off-peak calls.
	"deepseek-flash":         {"CNY", 2, 8, .04, 2},
	"deepseek-v4-pro":        {"CNY", 9, 27, .3, 9},
	"glm-5.3":                {"CNY", 8, 28, 2, 8},
	"glm-5.2":                {"CNY", 8, 28, 2, 8},
	"glm-5.3-flash":          {"CNY", .8, 2.8, .23, .8},
	"glm-5.3-flashx":         {"CNY", 2, 7, .57, 2},
	"glm-5v-turbo":           {"CNY", 5, 22, 1.2, 5},
	"kimi-k3":                {"CNY", 20, 100, 2, 20},
	"kimi-k2.8-preview":      {"CNY", 6.5, 27, 1.7, 6.5},
	"kimi-k2.7-code":         {"CNY", 6.5, 27, 1.3, 6.5},
	"kimi-k2.6":              {"CNY", 6.5, 27, 1.1, 6.5},
	"minimax-m3":             {"CNY", 2.1, 8.4, .42, 2.1},
	"minimax-m2.7":           {"CNY", 2.1, 8.4, .42, 2.1},
	"minimax-m3.1-flash":     {"CNY", 1, 4, .1, 1},
	"qwen3-coder-plus":       {"CNY", 4, 16, .4, 5},
	"qwen3-coder-flash":      {"CNY", 1, 4, .1, 1.25},
	"qwen3.8-max":            {"CNY", 12, 36, 1.2, 15},
	"gemini-3.1-pro-preview": {"USD", 2, 12, .2, 2},
	"gemini-4-argon":         {"USD", 2, 10, .1, 2},
}

var pricingSnapshotSuffix = regexp.MustCompile(`-(?:\d{8}|\d{4}-\d{2}-\d{2})$`)
var claudeVersionDots = regexp.MustCompile(`(claude-(?:opus|sonnet|haiku|fable)-\d+)\.(\d+)`)

func pricingModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimSuffix(strings.TrimSuffix(model, "-internal-ioa"), "-ioa")
	model = strings.TrimSuffix(model, "-1m")
	model = claudeVersionDots.ReplaceAllString(model, "$1-$2")
	model = pricingSnapshotSuffix.ReplaceAllString(model, "")
	switch model {
	case "deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4.1-flash", "deepseek-v4.1-flash-1017":
		return "deepseek-flash"
	case "kimi-k2.7":
		return "kimi-k2.7-code"
	case "gemini-3.1-pro":
		return "gemini-3.1-pro-preview"
	}
	return model
}

func IsHYModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "hy" || strings.HasPrefix(model, "hy-") || strings.HasPrefix(model, "hy.") || strings.HasPrefix(model, "hunyuan")
}

// AnalyticsPricedUsage never mutates stored billing facts. CostUSD in the
// projection is a USD equivalent of the CNY statistic for legacy consumers.
// Inclusive input (Codex/external API) includes caches; Anthropic input excludes them.
func AnalyticsPricedUsage(model string, usage TokenUsage, inclusiveInput bool) TokenUsage {
	out := usage
	out.CostUSD, out.CostStatus = 0, "unknown"
	if IsHYModel(model) {
		if usage.CostUSD >= 0 && !math.IsNaN(usage.CostUSD) && !math.IsInf(usage.CostUSD, 0) && (usage.CostStatus == "reported" || usage.CostUSD > 0 && usage.CostStatus != "unknown") {
			out.CostUSD, out.CostStatus = usage.CostUSD, "reported"
		}
		return out
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadTokens < 0 || usage.CacheWriteTokens < 0 || usage.ReasoningTokens < 0 {
		return out
	}
	if usage.InputTokens+usage.OutputTokens+usage.CacheReadTokens+usage.CacheWriteTokens == 0 {
		if usage.TotalTokens == 0 && usage.ReasoningTokens == 0 && usage.CostUSD == 0 {
			out.CostStatus = "estimated" // a script/unused node has no billable usage
		}
		return out
	}
	price, ok := publicTokenPrices[pricingModel(model)]
	if !ok {
		return out
	}
	input := usage.InputTokens
	if inclusiveInput {
		input -= usage.CacheReadTokens + usage.CacheWriteTokens
		if input < 0 {
			return out
		}
	}
	cost := (float64(input)*price.Input + float64(usage.OutputTokens)*price.Output + float64(usage.CacheReadTokens)*price.CacheRead + float64(usage.CacheWriteTokens)*price.CacheWrite) / 1e6
	if price.Currency == "CNY" {
		cost /= AnalyticsUSDToCNY
	}
	out.CostUSD, out.CostStatus = cost, "estimated"
	return out
}

package runtime

import (
	"slices"
	"strings"

	"agentflow/internal/domain"
)

// CursorModel is one model family of cursor-agent. The CLI has no effort
// flag; every reasoning effort is its own model ID, so the family picks the
// ID for the chosen effort.
type CursorModel struct {
	ID, Name string
	// Default is the effort cursor-agent presents as the plain model; empty
	// for a model without effort variants.
	Default string
	// Variants maps an effort to the cursor-agent model ID; the only key of
	// a model without effort variants is "".
	Variants map[string]string
}

// cursorModels mirrors `cursor-agent models` (2026.11). Fast variants are
// families of their own: speed is not a reasoning effort.
var cursorModels = []CursorModel{
	{ID: "auto", Name: "Auto", Default: "", Variants: map[string]string{"": "auto"}},
	{ID: "composer-2.5", Name: "Composer 2.5", Default: "", Variants: map[string]string{"": "composer-2.5"}},
	{ID: "composer-2.5-fast", Name: "Composer 2.5 Fast", Default: "", Variants: map[string]string{"": "composer-2.5-fast"}},
	{ID: "grok-4.7", Name: "Grok 4.7", Default: "medium", Variants: map[string]string{"low": "grok-4.7-low", "medium": "grok-4.7-medium", "high": "grok-4.7-high", "xhigh": "grok-4.7-xhigh"}},
	{ID: "grok-4.7-fast", Name: "Grok 4.7 Fast", Default: "medium", Variants: map[string]string{"low": "grok-4.7-low-fast", "medium": "grok-4.7-medium-fast", "high": "grok-4.7-high-fast", "xhigh": "grok-4.7-xhigh-fast"}},
	{ID: "cursor-grok-4.6", Name: "Grok 4.6", Default: "high", Variants: map[string]string{"low": "cursor-grok-4.6-low", "medium": "cursor-grok-4.6-medium", "high": "cursor-grok-4.6-high", "xhigh": "cursor-grok-4.6-xhigh"}},
	{ID: "cursor-grok-4.6-fast", Name: "Grok 4.6 Fast", Default: "high", Variants: map[string]string{"low": "cursor-grok-4.6-low-fast", "medium": "cursor-grok-4.6-medium-fast", "high": "cursor-grok-4.6-high-fast", "xhigh": "cursor-grok-4.6-xhigh-fast"}},
	{ID: "cursor-grok-4.5", Name: "Grok 4.5", Default: "high", Variants: map[string]string{"low": "cursor-grok-4.5-low", "medium": "cursor-grok-4.5-medium", "high": "cursor-grok-4.5-high"}},
	{ID: "cursor-grok-4.5-fast", Name: "Grok 4.5 Fast", Default: "high", Variants: map[string]string{"low": "cursor-grok-4.5-low-fast", "medium": "cursor-grok-4.5-medium-fast", "high": "cursor-grok-4.5-high-fast"}},
	{ID: "claude-fable-5-1", Name: "Claude Fable 5.1 (NO ZDR)", Default: "high", Variants: map[string]string{"low": "claude-fable-5-1-low", "medium": "claude-fable-5-1-medium", "high": "claude-fable-5-1-high", "xhigh": "claude-fable-5-1-xhigh", "max": "claude-fable-5-1-max"}},
	{ID: "claude-fable-5-1-thinking", Name: "Claude Fable 5.1 Thinking (NO ZDR)", Default: "high", Variants: map[string]string{"low": "claude-fable-5-1-thinking-low", "medium": "claude-fable-5-1-thinking-medium", "high": "claude-fable-5-1-thinking-high", "xhigh": "claude-fable-5-1-thinking-xhigh", "max": "claude-fable-5-1-thinking-max"}},
	{ID: "claude-fable-5", Name: "Claude Fable 5 (NO ZDR)", Default: "high", Variants: map[string]string{"low": "claude-fable-5-low", "medium": "claude-fable-5-medium", "high": "claude-fable-5-high", "xhigh": "claude-fable-5-xhigh", "max": "claude-fable-5-max"}},
	{ID: "claude-fable-5-thinking", Name: "Claude Fable 5 Thinking (NO ZDR)", Default: "high", Variants: map[string]string{"low": "claude-fable-5-thinking-low", "medium": "claude-fable-5-thinking-medium", "high": "claude-fable-5-thinking-high", "xhigh": "claude-fable-5-thinking-xhigh", "max": "claude-fable-5-thinking-max"}},
	{ID: "claude-opus-5-5", Name: "Claude Opus 5.5", Default: "medium", Variants: map[string]string{"low": "claude-opus-5-5-low", "medium": "claude-opus-5-5-medium", "high": "claude-opus-5-5-high", "xhigh": "claude-opus-5-5-xhigh", "max": "claude-opus-5-5-max"}},
	{ID: "claude-opus-5-5-fast", Name: "Claude Opus 5.5 Fast", Default: "medium", Variants: map[string]string{"low": "claude-opus-5-5-low-fast", "medium": "claude-opus-5-5-medium-fast", "high": "claude-opus-5-5-high-fast", "xhigh": "claude-opus-5-5-xhigh-fast", "max": "claude-opus-5-5-max-fast"}},
	{ID: "claude-opus-5", Name: "Claude Opus 5", Default: "high", Variants: map[string]string{"low": "claude-opus-5-low", "medium": "claude-opus-5-medium", "high": "claude-opus-5-high"}},
	{ID: "claude-opus-5-thinking", Name: "Claude Opus 5 Thinking", Default: "high", Variants: map[string]string{"low": "claude-opus-5-thinking-low", "medium": "claude-opus-5-thinking-medium", "high": "claude-opus-5-thinking-high", "xhigh": "claude-opus-5-thinking-xhigh", "max": "claude-opus-5-thinking-max"}},
	{ID: "claude-opus-5-fast", Name: "Claude Opus 5 Fast", Default: "high", Variants: map[string]string{"low": "claude-opus-5-low-fast", "medium": "claude-opus-5-medium-fast", "high": "claude-opus-5-high-fast"}},
	{ID: "claude-opus-5-thinking-fast", Name: "Claude Opus 5 Thinking Fast", Default: "high", Variants: map[string]string{"low": "claude-opus-5-thinking-low-fast", "medium": "claude-opus-5-thinking-medium-fast", "high": "claude-opus-5-thinking-high-fast", "xhigh": "claude-opus-5-thinking-xhigh-fast", "max": "claude-opus-5-thinking-max-fast"}},
	{ID: "claude-opus-4-8", Name: "Claude Opus 4.8", Default: "high", Variants: map[string]string{"low": "claude-opus-4-8-low", "medium": "claude-opus-4-8-medium", "high": "claude-opus-4-8-high", "xhigh": "claude-opus-4-8-xhigh", "max": "claude-opus-4-8-max"}},
	{ID: "claude-opus-4-8-thinking", Name: "Claude Opus 4.8 Thinking", Default: "high", Variants: map[string]string{"low": "claude-opus-4-8-thinking-low", "medium": "claude-opus-4-8-thinking-medium", "high": "claude-opus-4-8-thinking-high", "xhigh": "claude-opus-4-8-thinking-xhigh", "max": "claude-opus-4-8-thinking-max"}},
	{ID: "claude-opus-4-8-fast", Name: "Claude Opus 4.8 Fast", Default: "high", Variants: map[string]string{"low": "claude-opus-4-8-low-fast", "medium": "claude-opus-4-8-medium-fast", "high": "claude-opus-4-8-high-fast", "xhigh": "claude-opus-4-8-xhigh-fast", "max": "claude-opus-4-8-max-fast"}},
	{ID: "claude-opus-4-8-thinking-fast", Name: "Claude Opus 4.8 Thinking Fast", Default: "high", Variants: map[string]string{"low": "claude-opus-4-8-thinking-low-fast", "medium": "claude-opus-4-8-thinking-medium-fast", "high": "claude-opus-4-8-thinking-high-fast", "xhigh": "claude-opus-4-8-thinking-xhigh-fast", "max": "claude-opus-4-8-thinking-max-fast"}},
	{ID: "claude-opus-4-7", Name: "Claude Opus 4.7", Default: "xhigh", Variants: map[string]string{"low": "claude-opus-4-7-low", "medium": "claude-opus-4-7-medium", "high": "claude-opus-4-7-high", "xhigh": "claude-opus-4-7-xhigh", "max": "claude-opus-4-7-max"}},
	{ID: "claude-opus-4-7-thinking", Name: "Claude Opus 4.7 Thinking", Default: "xhigh", Variants: map[string]string{"low": "claude-opus-4-7-thinking-low", "medium": "claude-opus-4-7-thinking-medium", "high": "claude-opus-4-7-thinking-high", "xhigh": "claude-opus-4-7-thinking-xhigh", "max": "claude-opus-4-7-thinking-max"}},
	{ID: "claude-opus-4-7-fast", Name: "Claude Opus 4.7 Fast", Default: "xhigh", Variants: map[string]string{"low": "claude-opus-4-7-low-fast", "medium": "claude-opus-4-7-medium-fast", "high": "claude-opus-4-7-high-fast", "xhigh": "claude-opus-4-7-xhigh-fast", "max": "claude-opus-4-7-max-fast"}},
	{ID: "claude-opus-4-7-thinking-fast", Name: "Claude Opus 4.7 Thinking Fast", Default: "xhigh", Variants: map[string]string{"low": "claude-opus-4-7-thinking-low-fast", "medium": "claude-opus-4-7-thinking-medium-fast", "high": "claude-opus-4-7-thinking-high-fast", "xhigh": "claude-opus-4-7-thinking-xhigh-fast", "max": "claude-opus-4-7-thinking-max-fast"}},
	{ID: "claude-4.6-opus", Name: "Claude Opus 4.6", Default: "high", Variants: map[string]string{"high": "claude-4.6-opus-high", "max": "claude-4.6-opus-max"}},
	{ID: "claude-4.6-opus-thinking", Name: "Claude Opus 4.6 Thinking", Default: "high", Variants: map[string]string{"high": "claude-4.6-opus-high-thinking", "max": "claude-4.6-opus-max-thinking"}},
	{ID: "claude-4.5-opus", Name: "Claude Opus 4.5", Default: "high", Variants: map[string]string{"high": "claude-4.5-opus-high"}},
	{ID: "claude-4.5-opus-thinking", Name: "Claude Opus 4.5 Thinking", Default: "high", Variants: map[string]string{"high": "claude-4.5-opus-high-thinking"}},
	{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5", Default: "medium", Variants: map[string]string{"low": "claude-sonnet-5-5-low", "medium": "claude-sonnet-5-5-medium", "high": "claude-sonnet-5-5-high", "xhigh": "claude-sonnet-5-5-xhigh", "max": "claude-sonnet-5-5-max"}},
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", Default: "high", Variants: map[string]string{"low": "claude-sonnet-5-low", "medium": "claude-sonnet-5-medium", "high": "claude-sonnet-5-high", "xhigh": "claude-sonnet-5-xhigh", "max": "claude-sonnet-5-max"}},
	{ID: "claude-sonnet-5-thinking", Name: "Claude Sonnet 5 Thinking", Default: "high", Variants: map[string]string{"low": "claude-sonnet-5-thinking-low", "medium": "claude-sonnet-5-thinking-medium", "high": "claude-sonnet-5-thinking-high", "xhigh": "claude-sonnet-5-thinking-xhigh", "max": "claude-sonnet-5-thinking-max"}},
	{ID: "claude-4.6-sonnet", Name: "Claude Sonnet 4.6", Default: "medium", Variants: map[string]string{"medium": "claude-4.6-sonnet-medium"}},
	{ID: "claude-4.6-sonnet-thinking", Name: "Claude Sonnet 4.6 Thinking", Default: "medium", Variants: map[string]string{"medium": "claude-4.6-sonnet-medium-thinking"}},
	{ID: "claude-4.5-sonnet", Name: "Claude Sonnet 4.5", Default: "", Variants: map[string]string{"": "claude-4.5-sonnet"}},
	{ID: "claude-4.5-sonnet-thinking", Name: "Claude Sonnet 4.5 Thinking", Default: "", Variants: map[string]string{"": "claude-4.5-sonnet-thinking"}},
	{ID: "claude-4-sonnet", Name: "Claude Sonnet 4", Default: "", Variants: map[string]string{"": "claude-4-sonnet"}},
	{ID: "claude-4-sonnet-thinking", Name: "Claude Sonnet 4 Thinking", Default: "", Variants: map[string]string{"": "claude-4-sonnet-thinking"}},
	{ID: "claude-haiku-5-5", Name: "Claude Haiku 5.5", Default: "medium", Variants: map[string]string{"low": "claude-haiku-5-5-low", "medium": "claude-haiku-5-5-medium", "high": "claude-haiku-5-5-high", "xhigh": "claude-haiku-5-5-xhigh", "max": "claude-haiku-5-5-max"}},
	{ID: "claude-haiku-5-5-thinking", Name: "Claude Haiku 5.5 Thinking", Default: "medium", Variants: map[string]string{"low": "claude-haiku-5-5-thinking-low", "medium": "claude-haiku-5-5-thinking-medium", "high": "claude-haiku-5-5-thinking-high", "xhigh": "claude-haiku-5-5-thinking-xhigh", "max": "claude-haiku-5-5-thinking-max"}},
	{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", Default: "medium", Variants: map[string]string{"none": "gpt-5.6-sol-none", "low": "gpt-5.6-sol-low", "medium": "gpt-5.6-sol-medium", "high": "gpt-5.6-sol-high", "xhigh": "gpt-5.6-sol-xhigh", "max": "gpt-5.6-sol-max"}},
	{ID: "gpt-5.6-sol-fast", Name: "GPT-5.6 Sol Fast", Default: "medium", Variants: map[string]string{"none": "gpt-5.6-sol-none-fast", "low": "gpt-5.6-sol-low-fast", "medium": "gpt-5.6-sol-medium-fast", "high": "gpt-5.6-sol-high-fast", "xhigh": "gpt-5.6-sol-xhigh-fast", "max": "gpt-5.6-sol-max-fast"}},
	{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Default: "medium", Variants: map[string]string{"none": "gpt-5.6-terra-none", "low": "gpt-5.6-terra-low", "medium": "gpt-5.6-terra-medium", "high": "gpt-5.6-terra-high", "xhigh": "gpt-5.6-terra-xhigh", "max": "gpt-5.6-terra-max"}},
	{ID: "gpt-5.6-terra-fast", Name: "GPT-5.6 Terra Fast", Default: "medium", Variants: map[string]string{"none": "gpt-5.6-terra-none-fast", "low": "gpt-5.6-terra-low-fast", "medium": "gpt-5.6-terra-medium-fast", "high": "gpt-5.6-terra-high-fast", "xhigh": "gpt-5.6-terra-xhigh-fast", "max": "gpt-5.6-terra-max-fast"}},
	{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", Default: "medium", Variants: map[string]string{"none": "gpt-5.6-luna-none", "low": "gpt-5.6-luna-low", "medium": "gpt-5.6-luna-medium", "high": "gpt-5.6-luna-high", "xhigh": "gpt-5.6-luna-xhigh", "max": "gpt-5.6-luna-max"}},
	{ID: "gpt-5.6-luna-fast", Name: "GPT-5.6 Luna Fast", Default: "medium", Variants: map[string]string{"none": "gpt-5.6-luna-none-fast", "low": "gpt-5.6-luna-low-fast", "medium": "gpt-5.6-luna-medium-fast", "high": "gpt-5.6-luna-high-fast", "xhigh": "gpt-5.6-luna-xhigh-fast", "max": "gpt-5.6-luna-max-fast"}},
	{ID: "gpt-5.5", Name: "GPT-5.5", Default: "medium", Variants: map[string]string{"none": "gpt-5.5-none", "low": "gpt-5.5-low", "medium": "gpt-5.5-medium", "high": "gpt-5.5-high", "xhigh": "gpt-5.5-extra-high"}},
	{ID: "gpt-5.5-fast", Name: "GPT-5.5 Fast", Default: "medium", Variants: map[string]string{"none": "gpt-5.5-none-fast", "low": "gpt-5.5-low-fast", "medium": "gpt-5.5-medium-fast", "high": "gpt-5.5-high-fast", "xhigh": "gpt-5.5-extra-high-fast"}},
	{ID: "gpt-5.4", Name: "GPT-5.4", Default: "medium", Variants: map[string]string{"low": "gpt-5.4-low", "medium": "gpt-5.4-medium", "high": "gpt-5.4-high", "xhigh": "gpt-5.4-xhigh"}},
	{ID: "gpt-5.4-fast", Name: "GPT-5.4 Fast", Default: "medium", Variants: map[string]string{"medium": "gpt-5.4-medium-fast", "high": "gpt-5.4-high-fast", "xhigh": "gpt-5.4-xhigh-fast"}},
	{ID: "gpt-5.4-mini", Name: "GPT-5.4 Mini", Default: "medium", Variants: map[string]string{"none": "gpt-5.4-mini-none", "low": "gpt-5.4-mini-low", "medium": "gpt-5.4-mini-medium", "high": "gpt-5.4-mini-high", "xhigh": "gpt-5.4-mini-xhigh"}},
	{ID: "gpt-5.4-nano", Name: "GPT-5.4 Nano", Default: "medium", Variants: map[string]string{"none": "gpt-5.4-nano-none", "low": "gpt-5.4-nano-low", "medium": "gpt-5.4-nano-medium", "high": "gpt-5.4-nano-high", "xhigh": "gpt-5.4-nano-xhigh"}},
	{ID: "gpt-5.3-codex", Name: "Codex 5.3", Default: "medium", Variants: map[string]string{"low": "gpt-5.3-codex-low", "medium": "gpt-5.3-codex", "high": "gpt-5.3-codex-high", "xhigh": "gpt-5.3-codex-xhigh"}},
	{ID: "gpt-5.3-codex-fast", Name: "Codex 5.3 Fast", Default: "medium", Variants: map[string]string{"low": "gpt-5.3-codex-low-fast", "medium": "gpt-5.3-codex-fast", "high": "gpt-5.3-codex-high-fast", "xhigh": "gpt-5.3-codex-xhigh-fast"}},
	{ID: "gpt-5.2", Name: "GPT-5.2", Default: "medium", Variants: map[string]string{"low": "gpt-5.2-low", "medium": "gpt-5.2", "high": "gpt-5.2-high", "xhigh": "gpt-5.2-xhigh"}},
	{ID: "gpt-5.2-fast", Name: "GPT-5.2 Fast", Default: "medium", Variants: map[string]string{"low": "gpt-5.2-low-fast", "medium": "gpt-5.2-fast", "high": "gpt-5.2-high-fast", "xhigh": "gpt-5.2-xhigh-fast"}},
	{ID: "gpt-5.1", Name: "GPT-5.1", Default: "medium", Variants: map[string]string{"low": "gpt-5.1-low", "medium": "gpt-5.1", "high": "gpt-5.1-high"}},
	{ID: "gpt-5-mini", Name: "GPT-5 Mini", Default: "", Variants: map[string]string{"": "gpt-5-mini"}},
	{ID: "gemini-4-carbon", Name: "Gemini 4 Carbon", Default: "medium", Variants: map[string]string{"low": "gemini-4-carbon-low", "medium": "gemini-4-carbon-medium", "high": "gemini-4-carbon-high"}},
	{ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen", Default: "medium", Variants: map[string]string{"low": "gemini-4-hydrogen-low", "medium": "gemini-4-hydrogen-medium", "high": "gemini-4-hydrogen-high", "xhigh": "gemini-4-hydrogen-xhigh"}},
	{ID: "gemini-4-argon", Name: "Gemini 4 Argon", Default: "medium", Variants: map[string]string{"low": "gemini-4-argon-low", "medium": "gemini-4-argon-medium", "high": "gemini-4-argon-high"}},
	{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash", Default: "medium", Variants: map[string]string{"low": "gemini-3.8-flash-low", "medium": "gemini-3.8-flash-medium", "high": "gemini-3.8-flash-high"}},
	{ID: "gemini-3.7-flash", Name: "Gemini 3.7 Flash", Default: "high", Variants: map[string]string{"low": "gemini-3.7-flash-low", "medium": "gemini-3.7-flash-medium", "high": "gemini-3.7-flash-high"}},
	{ID: "gemini-3.6-flash", Name: "Gemini 3.6 Flash", Default: "high", Variants: map[string]string{"minimal": "gemini-3.6-flash-minimal", "low": "gemini-3.6-flash-low", "medium": "gemini-3.6-flash-medium", "high": "gemini-3.6-flash-high"}},
	{ID: "gemini-3.5-flash", Name: "Gemini 3.5 Flash", Default: "", Variants: map[string]string{"": "gemini-3.5-flash"}},
	{ID: "gemini-3.1-pro", Name: "Gemini 3.1 Pro", Default: "", Variants: map[string]string{"": "gemini-3.1-pro"}},
	{ID: "gemini-3-flash", Name: "Gemini 3 Flash", Default: "", Variants: map[string]string{"": "gemini-3-flash"}},
	{ID: "kimi-k3", Name: "Kimi K3", Default: "max", Variants: map[string]string{"low": "kimi-k3-low", "high": "kimi-k3-high", "max": "kimi-k3-max"}},
	{ID: "kimi-k2.7-code", Name: "Kimi K2.7 Code", Default: "", Variants: map[string]string{"": "kimi-k2.7-code"}},
	{ID: "deepseek-v41-flash-1017", Name: "DeepSeek V4.1 Flash 1017", Default: "medium", Variants: map[string]string{"none": "deepseek-v41-flash-1017-none", "low": "deepseek-v41-flash-1017-low", "medium": "deepseek-v41-flash-1017-medium", "high": "deepseek-v41-flash-1017-high", "max": "deepseek-v41-flash-1017-max"}},
	{ID: "glm-5.2", Name: "GLM 5.2", Default: "high", Variants: map[string]string{"high": "glm-5.2-high", "max": "glm-5.2-max"}},
	{ID: "muse-spark-1.3", Name: "Muse Spark 1.3", Default: "high", Variants: map[string]string{"minimal": "muse-spark-1.3-minimal", "low": "muse-spark-1.3-low", "medium": "muse-spark-1.3-medium", "high": "muse-spark-1.3-high", "xhigh": "muse-spark-1.3-xhigh", "max": "muse-spark-1.3-max"}},
	{ID: "muse-wonder-1.0", Name: "Muse Wonder", Default: "medium", Variants: map[string]string{"minimal": "muse-wonder-1.0-minimal", "low": "muse-wonder-1.0-low", "medium": "muse-wonder-1.0-medium", "high": "muse-wonder-1.0-high", "xhigh": "muse-wonder-1.0-xhigh", "max": "muse-wonder-1.0-max"}},
}

var cursorModelByID, cursorModelByVariant = func() (map[string]CursorModel, map[string]CursorModel) {
	byID, byVariant := map[string]CursorModel{}, map[string]CursorModel{}
	for _, model := range cursorModels {
		byID[model.ID] = model
		for _, id := range model.Variants {
			byVariant[id] = model
		}
	}
	return byID, byVariant
}()

// CursorModels is the built-in family table, in display order.
func CursorModels() []CursorModel { return slices.Clone(cursorModels) }

// CursorModelFamily finds the family a cursor-agent model ID belongs to.
func CursorModelFamily(id string) (CursorModel, bool) {
	model, ok := cursorModelByVariant[id]
	return model, ok
}

// CursorModelEfforts lists the efforts of a family, weakest first; nil for
// a model ID outside the known families or without effort variants. catalog
// is the owner's saved Cursor model list: families folded out of their CLI's
// own list (FoldCursorCLIModels) resolve from it.
func CursorModelEfforts(id string, catalog []domain.CLIModel) []string {
	model, ok := lookupCursorFamily(id, catalog)
	if !ok {
		return nil
	}
	var out []string
	for _, effort := range domain.ExecutorEffortLevels[AgentCLICursor] {
		if _, ok := model.Variants[effort]; ok {
			out = append(out, effort)
		}
	}
	return out
}

// CursorModelID resolves a family and effort to the cursor-agent model ID,
// looking the family up in the built-in table, then in catalog (see
// CursorModelEfforts). An empty effort takes the family default and an
// effort the family lacks the closest one (the stronger on a tie). Any other
// ID passes through unchanged: only a family known to have an effort may
// gain its suffix, otherwise the CLI would get an ID it never listed.
func CursorModelID(id, effort string, catalog []domain.CLIModel) string {
	if model, ok := lookupCursorFamily(id, catalog); ok {
		return cursorVariantID(model, effort)
	}
	return id
}

func cursorVariantID(model CursorModel, effort string) string {
	if raw, ok := model.Variants[""]; ok {
		return raw
	}
	if raw, ok := model.Variants[effort]; ok {
		return raw
	}
	levels := domain.ExecutorEffortLevels[AgentCLICursor]
	want := slices.Index(levels, effort)
	if want < 0 {
		return model.Variants[model.Default]
	}
	best, distance := model.Default, len(levels)
	for i, level := range levels {
		if _, ok := model.Variants[level]; ok && abs(i-want) <= distance {
			best, distance = level, abs(i-want)
		}
	}
	return model.Variants[best]
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// cursorEffortSuffixes are the trailing tokens cursor-agent appends to a
// family ID. Longer tokens come first so xhigh is not read as high.
// extra-high is the spelling some models use for xhigh.
var cursorEffortSuffixes = []struct{ token, effort string }{
	{"extra-high", "xhigh"},
	{"minimal", "minimal"},
	{"medium", "medium"},
	{"xhigh", "xhigh"},
	{"ultra", "ultra"},
	{"high", "high"},
	{"none", "none"},
	{"max", "max"},
	{"low", "low"},
}

var cursorEffortNameSuffixes = []string{"Extra High", "Minimal", "Medium", "Xhigh", "Ultra", "High", "None", "Max", "Low"}

func lookupCursorFamily(id string, catalog []domain.CLIModel) (CursorModel, bool) {
	if model, ok := cursorModelByID[id]; ok {
		return model, true
	}
	for _, entry := range catalog {
		if entry.ID != id || len(entry.Efforts) == 0 {
			continue
		}
		variants := map[string]string{}
		for effort, raw := range entry.Efforts {
			if cursorEffortToken(effort) != "" && strings.TrimSpace(raw) != "" {
				variants[effort] = strings.TrimSpace(raw)
			}
		}
		if len(variants) == 0 {
			return CursorModel{}, false
		}
		def := entry.DefaultEffort
		if _, ok := variants[def]; !ok {
			def = inferredCursorDefault(variants)
		}
		return CursorModel{ID: id, Name: entry.Name, Default: def, Variants: variants}, true
	}
	return CursorModel{}, false
}

func cursorEffortToken(effort string) string {
	switch strings.TrimSpace(effort) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return effort
	default:
		return ""
	}
}

// splitCursorEffortID reports the family and effort of a cursor-agent model
// ID. A trailing -fast stays on the family: speed is its own family.
func splitCursorEffortID(id string) (family, effort string, ok bool) {
	fast := false
	if base, found := strings.CutSuffix(id, "-fast"); found && base != "" {
		id, fast = base, true
	}
	for _, suffix := range cursorEffortSuffixes {
		base, found := strings.CutSuffix(id, "-"+suffix.token)
		if !found || base == "" {
			continue
		}
		if fast {
			base += "-fast"
		}
		return base, suffix.effort, true
	}
	return "", "", false
}

// stripCursorEffortName drops the effort word from a cursor-agent model
// name, keeping a trailing Fast and any trailing notes in parentheses:
// "Gemini 4 Carbon Medium (NO ZDR)" names the family "Gemini 4 Carbon (NO ZDR)".
func stripCursorEffortName(name string) string {
	name = strings.TrimSpace(name)
	notes := ""
	for strings.HasSuffix(name, ")") {
		open := strings.LastIndex(name, " (")
		if open <= 0 {
			break
		}
		notes = name[open:] + notes
		name = strings.TrimSpace(name[:open])
	}
	fast := false
	if base, found := strings.CutSuffix(name, " Fast"); found && strings.TrimSpace(base) != "" {
		name, fast = strings.TrimSpace(base), true
	}
	for _, suffix := range cursorEffortNameSuffixes {
		if len(name) <= len(suffix)+1 || name[len(name)-len(suffix)-1] != ' ' {
			continue
		}
		if strings.EqualFold(name[len(name)-len(suffix):], suffix) {
			name = strings.TrimSpace(name[:len(name)-len(suffix)])
			break
		}
	}
	if fast {
		name += " Fast"
	}
	return name + notes
}

func inferredCursorDefault(variants map[string]string) string {
	if _, ok := variants["medium"]; ok {
		return "medium"
	}
	levels := domain.ExecutorEffortLevels[AgentCLICursor]
	want := slices.Index(levels, "medium")
	best, distance, bestIndex := "", len(levels)+1, -1
	for i, level := range levels {
		if _, ok := variants[level]; !ok {
			continue
		}
		gap := abs(i - want)
		if best == "" || gap < distance || (gap == distance && i > bestIndex) {
			best, distance, bestIndex = level, gap, i
		}
	}
	return best
}

// FoldCursorCLIModels collapses cursor-agent's per-effort IDs into one row
// per family. Families in the built-in table keep that table's name and
// order. Other IDs ending in a recognized effort suffix fold the same way,
// and the rest stay as the CLI listed them. Such a folded row carries its
// effort→ID mapping (Efforts, DefaultEffort); it is saved with the owner's
// Cursor catalog, which CursorModelID and CursorModelEfforts resolve from.
func FoldCursorCLIModels(models []domain.CLIModel) []domain.CLIModel {
	present, groups, tail := scanCursorCLIModels(models)
	dynamic := mergePlainCursorFamilies(tail, groups)
	out := make([]domain.CLIModel, 0, len(present)+len(dynamic))
	for _, family := range cursorModels {
		if present[family.ID] {
			out = append(out, domain.CLIModel{ID: family.ID, Name: family.Name})
		}
	}
	for _, item := range dynamic {
		if !item.family {
			out = append(out, item.plain)
			continue
		}
		out = append(out, cursorFamilyRow(item.id, groups[item.id]))
	}
	return out
}

// cursorFamilyGroup collects the effort variants of one family that is not
// in the built-in table.
type cursorFamilyGroup struct {
	name     string
	variants map[string]string
}

// cursorModelSlot keeps the CLI's listing order of a dynamic family or of a
// plain model ID.
type cursorModelSlot struct {
	family bool
	id     string
	plain  domain.CLIModel
}

// scanCursorCLIModels sorts the listed IDs into built-in families (present),
// dynamic families folded from effort suffixes (groups), and the listing
// order of dynamic families and plain IDs (tail).
func scanCursorCLIModels(models []domain.CLIModel) (map[string]bool, map[string]*cursorFamilyGroup, []cursorModelSlot) {
	present := map[string]bool{}
	groups := map[string]*cursorFamilyGroup{}
	var tail []cursorModelSlot
	seen := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if family, ok := CursorModelFamily(id); ok {
			present[family.ID] = true
			continue
		}
		if familyID, effort, ok := splitCursorEffortID(id); ok {
			if _, known := cursorModelByID[familyID]; known {
				present[familyID] = true
				continue
			}
			g := groups[familyID]
			if g == nil {
				g = &cursorFamilyGroup{variants: map[string]string{}}
				groups[familyID] = g
				if !seen["family:"+familyID] {
					seen["family:"+familyID] = true
					tail = append(tail, cursorModelSlot{family: true, id: familyID})
				}
			}
			if _, exists := g.variants[effort]; !exists {
				g.variants[effort] = id
			}
			if stripped := stripCursorEffortName(model.Name); stripped != "" && (g.name == "" || effort == "medium") {
				g.name = stripped
			}
			continue
		}
		if seen["plain:"+id] {
			continue
		}
		seen["plain:"+id] = true
		tail = append(tail, cursorModelSlot{id: id, plain: domain.CLIModel{ID: id, Name: strings.TrimSpace(model.Name)}})
	}
	return present, groups, tail
}

// mergePlainCursorFamilies drops a plain ID that names a dynamic family and
// records it as that family's medium variant unless one was listed; its name
// then becomes the family name.
func mergePlainCursorFamilies(tail []cursorModelSlot, groups map[string]*cursorFamilyGroup) []cursorModelSlot {
	var dynamic []cursorModelSlot
	for _, item := range tail {
		if item.family {
			dynamic = append(dynamic, item)
			continue
		}
		g := groups[item.id]
		if g == nil {
			dynamic = append(dynamic, item)
			continue
		}
		if _, ok := g.variants["medium"]; !ok {
			g.variants["medium"] = item.id
			if item.plain.Name != "" {
				g.name = item.plain.Name
			}
		}
	}
	return dynamic
}

// cursorFamilyRow builds the saved row of a dynamic family, falling back to
// the family ID when no name was found.
func cursorFamilyRow(id string, g *cursorFamilyGroup) domain.CLIModel {
	name := g.name
	if name == "" {
		name = id
	}
	variants := make(map[string]string, len(g.variants))
	for effort, raw := range g.variants {
		variants[effort] = raw
	}
	return domain.CLIModel{ID: id, Name: name, Efforts: variants, DefaultEffort: inferredCursorDefault(variants)}
}

package httpapi

import (
	"fmt"
	"slices"
	"strings"
)

// Mirrors CodeBuddy Code 2.177.1's product reasoning.supportedEfforts and
// catalog effort capability. Unknown models use the conservative low/medium/high
// fallback: the CLI clamps xhigh/max to high when no capability is known.
// Keep this snapshot alongside the fixed chat model catalog when updating it.
// Empty effort always leaves the model's native default untouched.
func assistantModelEfforts(model string) []string {
	switch model {
	case "hy4-preview-ioa":
		return []string{"high"}
	case "glm-5.3-flash-ioa", "glm-5.3-flashx":
		return []string{"low", "high", "max"}
	case "deepseek-v4.1-flash", "gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"claude-sonnet-5", "claude-sonnet-5-1m", "claude-opus-5":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "gpt-5.5", "gpt-5.4", "gpt-5.3-codex":
		return []string{"low", "medium", "high", "xhigh"}
	default:
		return []string{"low", "medium", "high"}
	}
}

func validateAssistantEffort(model, effort string) error {
	if effort == "" {
		return nil
	}
	if !slices.Contains(assistantModelEfforts(model), effort) {
		return fmt.Errorf("模型 %s 不支持思考强度 %q，可选：默认、%s", model, effort, strings.Join(assistantModelEfforts(model), "、"))
	}
	return nil
}

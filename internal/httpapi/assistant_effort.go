package httpapi

import (
	"fmt"
	"slices"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// Mirrors CodeBuddy Code 2.180.0's product reasoning.supportedEfforts and
// catalog effort capability. Unknown models use the conservative low/medium/high
// fallback: the CLI clamps xhigh/max to high when no capability is known.
// Keep this snapshot alongside the fixed chat model catalog when updating it.
// Empty effort always leaves the model's native default untouched.
//
// user's saved Cursor catalog resolves the Cursor families folded out of
// their CLI's own model list (runtime.FoldCursorCLIModels).
func assistantModelEfforts(user domain.User, model string) []string {
	engine, _ := assistantModelEngine(model)
	catalog := assistantEngineCustomModels(user, engine)
	if len(catalog) == 0 {
		key, baseURL := assistantEngineCredential(user, engine)
		anthropic := assistantEngineAnthropicModels(engine, assistantEngineProtocol(user, engine))
		if cached, ok := assistantDiscoveryCache.Load(assistantDiscoveryKey(anthropic, key, baseURL)); ok {
			catalog = cliModelsOf(cached.(assistantDiscoveryEntry).models)
		}
	}
	return assistantModelEffortsIn(model, catalog)
}

// assistantModelEffortsIn is assistantModelEfforts with the engine's catalog
// given directly.
func assistantModelEffortsIn(model string, catalog []domain.CLIModel) []string {
	engine, id := assistantModelEngine(model)
	supported := domain.ExecutorEffortLevels[engine]
	if engine == runtime.AgentCLICursor {
		supported = runtime.CursorModelEfforts(id, catalog)
	} else if engine == runtime.AgentCLIAntigravity {
		supported = runtime.AgyModelEfforts(id)
	}
	for _, item := range catalog {
		if item.ID != id || item.ReasoningEfforts == nil {
			continue
		}
		levels := []string{}
		for _, level := range supported {
			if slices.Contains(*item.ReasoningEfforts, level) {
				levels = append(levels, level)
			}
		}
		return levels
	}
	if runtime.IsAgentCLI(engine) {
		return supported
	}
	switch id {
	case "hy4std", "hy4-preview-ioa":
		return []string{"high"}
	case "glm-5.3-ioa", "glm-5.3-flash-ioa", "glm-5.3-flashx", "kimi-k3.1-pro", "kimi-k2.8-preview":
		return []string{"low", "high", "max"}
	case "deepseek-v4.1-flash", "gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"claude-sonnet-5", "claude-sonnet-5-1m", "claude-opus-5", "claude-opus-5.5", "claude-haiku-5.5",
		"minimax-m3.1-flash":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna":
		return []string{"low", "medium", "high", "xhigh"}
	default:
		return []string{"low", "medium", "high"}
	}
}

func validateAssistantEffort(user domain.User, model, effort string) error {
	if effort == "" {
		return nil
	}
	efforts := assistantModelEfforts(user, model)
	if !slices.Contains(efforts, effort) {
		if len(efforts) == 0 {
			return fmt.Errorf("模型 %s 仅支持默认思考强度", model)
		}
		return fmt.Errorf("模型 %s 不支持思考强度 %q，可选：默认、%s", model, effort, strings.Join(efforts, "、"))
	}
	return nil
}

// assistantCursorCatalog is the user's saved Cursor model list.
func assistantCursorCatalog(user domain.User) []domain.CLIModel {
	return user.AgentCLICredentials[runtime.AgentCLICursor].Models
}

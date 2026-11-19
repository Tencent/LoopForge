package httpapi

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// assistantModelEfforts resolves the same capabilities as the picker for
// custom gateways. Cursor uses the saved family mapping that drives its CLI.
func assistantModelEfforts(ctx context.Context, user domain.User, model string) []string {
	engine, _ := assistantModelEngine(model)
	if engine == assistantEngineCodeBuddy || engine == runtime.AgentCLICursor {
		return assistantModelEffortsIn(model, assistantEngineCustomModels(user, engine))
	}
	_, baseURL := assistantEngineCredential(user, engine)
	if engine == runtime.AgentCLIKimi && baseURL != "" {
		return nil
	}
	models, _ := assistantDiscoveredModelsFor(ctx, user, engine)
	return assistantModelEffortsIn(model, cliModelsOf(models))
}

func assistantModelEffortsForBaseURL(model string, catalog []domain.CLIModel, baseURL string) []string {
	if baseURL != "" && strings.HasPrefix(strings.TrimSpace(model), runtime.AgentCLIKimi+":") {
		return nil
	}
	return assistantModelEffortsIn(model, catalog)
}

// assistantModelEffortsIn prefers configured levels, then discovered levels,
// then the built-in model or CLI capabilities.
// The CodeBuddy fallback mirrors CodeBuddy Code 2.180.0's model capabilities.
// Unknown CodeBuddy, Codex and Claude Code models fall back to low/medium/high.
func assistantModelEffortsIn(model string, catalog []domain.CLIModel) []string {
	return assistantModelEffortsInCatalog(model, catalog, false)
}

func assistantModelEffortsInCatalog(model string, catalog []domain.CLIModel, runnerLogin bool) []string {
	engine, id := assistantModelEngine(model)
	supported := domain.ExecutorEffortLevels[engine]
	builtins := builtinExecutorModels[engine]
	builtin := slices.IndexFunc(builtins, func(model builtinExecutorModel) bool { return model.id == id })
	if engine == runtime.AgentCLICursor {
		supported = runtime.CursorModelEfforts(id, catalog)
	} else if engine == runtime.AgentCLIAntigravity {
		supported = runtime.AgyModelEfforts(id)
	} else if engine == runtime.AgentCLIKimi && builtin >= 0 {
		supported = builtins[builtin].efforts
	}
	for _, item := range catalog {
		if item.ID != id {
			continue
		}
		configured := item.ReasoningEfforts
		if configured == nil {
			configured = item.DiscoveredReasoningEfforts
		}
		if runnerLogin && engine == runtime.AgentCLIAntigravity && len(item.Efforts) > 0 {
			supported = nil
			for _, level := range runtime.AgyAccountModelEfforts(id) {
				if item.Efforts[level] != "" {
					supported = append(supported, level)
				}
			}
			if configured == nil {
				return supported
			}
		}
		if configured == nil {
			continue
		}
		advertised := *configured
		if engine == runtime.AgentCLIKimi || engine == runtime.AgentCLIDeepSeek {
			advertised = slices.Clone(advertised)
			for i, level := range advertised {
				switch level {
				case "off":
					advertised[i] = "none"
				case "on":
					advertised[i] = "thinking"
				}
			}
		}
		levels := []string{}
		for _, level := range supported {
			if slices.Contains(advertised, level) {
				levels = append(levels, level)
			}
		}
		return levels
	}
	if builtin >= 0 {
		return builtins[builtin].efforts
	}
	if runtime.IsAgentCLI(engine) {
		if len(builtins) > 0 {
			return nil
		}
		return supported
	}
	if engine == assistantEngineCodex {
		return []string{"low", "medium", "high"}
	}
	// Keep the legacy fallback for CodeBuddy and custom Claude gateways.
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

func validateAssistantEffort(ctx context.Context, user domain.User, model, effort string) error {
	if effort == "" {
		return nil
	}
	return validateAssistantModelEffort(model, effort, assistantModelEfforts(ctx, user, model))
}

type assistantRunnerTurnModelKey struct{}

// Carry the admitted selection into the internal chat request of this turn.
type assistantRunnerTurnModel struct {
	runnerID string
	model    string
	effort   string
	resolved string
}

func (h *assistantHandlers) resolveAssistantTurnModel(ctx context.Context, user domain.User, model, effort string, runner domain.Runner) (string, error) {
	engine, id := assistantModelEngine(model)
	if runner.ID == "" || engine != runtime.AgentCLIAntigravity {
		return model, validateAssistantEffort(ctx, user, model, effort)
	}
	// Legacy sessions can carry an explicit variant in the ID alone.
	_, variantEffort := runtime.SplitAgyModelEffort(id)
	if effort == "" && variantEffort == "" {
		return model, nil
	}
	if admitted, ok := ctx.Value(assistantRunnerTurnModelKey{}).(assistantRunnerTurnModel); ok &&
		admitted.runnerID == runner.ID && admitted.model == model && admitted.effort == effort {
		return admitted.resolved, nil
	}
	_, catalog := runnerLoginModelCatalog(ctx, h.runnerControls, runner, engine, assistantEngineCustomModels(user, engine))
	if catalog.Error != "" {
		return "", fmt.Errorf("无法验证模型 %s 的思考强度：%s", model, catalog.Error)
	}
	for _, item := range catalog.Models {
		if item.ID != id && (variantEffort == "" || item.Efforts[variantEffort] != id) {
			continue
		}
		if effort == "" {
			return model, nil
		}
		efforts := assistantModelEffortsInCatalog(engine+":"+item.ID, catalog.Models, true)
		if err := validateAssistantModelEffort(model, effort, efforts); err != nil {
			return "", err
		}
		return engine + ":" + firstNonEmpty(item.Efforts[effort], id), nil
	}
	return "", fmt.Errorf("模型 %s 已不在该执行机器的账号目录，请重新选择模型", model)
}

func validateAssistantModelEffort(model, effort string, efforts []string) error {
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

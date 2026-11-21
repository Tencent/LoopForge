package httpapi

import (
	"errors"
	"strings"

	"agentflow/internal/domain"
)

// A bare concrete ID is the legacy CodeBuddy format. Only an omitted model
// or unqualified "default" can select another CLI based on credentials.
func normalizeFeatureModel(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return assistantDefaultModel, nil
	}
	engine, model := assistantModelEngine(requested)
	if !assistantValidEngineModel(model) {
		return "", errFeatureModel
	}
	if engine == assistantEngineCodeBuddy && !assistantModelHasEnginePrefix(requested) {
		return model, nil
	}
	return engine + ":" + model, nil
}

var (
	errFeatureModel       = errors.New("请选择有效的 CLI 模型")
	errFeatureCredentials = errors.New("请先在个人配置中配置至少一个 CLI 的凭据")
)

func assistantModelUnspecified(model string) bool {
	model = strings.TrimSpace(model)
	return model == "" || model == assistantDefaultModel
}

func personalDefaultModel(user domain.User) string {
	engine := user.AssistantDefaults.Executor
	if engine != "" {
		if key, _ := assistantEngineCredential(user, engine); key != "" {
			return featureDefaultModel(user, engine)
		}
	}
	return ""
}

func resolveFeatureModel(user domain.User, requested string) (string, error) {
	model, err := normalizeFeatureModel(requested)
	if err != nil {
		return "", err
	}
	if model == assistantDefaultModel {
		if preferred := personalDefaultModel(user); preferred != "" {
			return preferred, nil
		}
		for _, option := range assistantEngineOptions(user, false) {
			if option.Configured {
				return featureDefaultModel(user, option.ID), nil
			}
		}
		return "", errFeatureCredentials
	}
	engine, id := assistantModelEngine(model)
	if key, _ := assistantEngineCredential(user, engine); key == "" {
		return "", errors.New(assistantEngineMissingKey(engine))
	}
	if id == assistantDefaultModel || engine == assistantEngineCodeBuddy && codeBuddyIsDefaultModel(id) {
		return featureDefaultModel(user, engine), nil
	}
	return model, nil
}

func featureDefaultModel(user domain.User, engine string) string {
	if defaults := user.AssistantDefaults; defaults.Executor == engine && defaults.Model != "" {
		return engine + ":" + defaults.Model
	}
	if engine == assistantEngineCodeBuddy {
		// These features previously used the deployment's CodeBuddy default.
		return engine + ":" + assistantFixedCatalog().Default
	}
	if models := assistantSavedModels(user, engine); len(models) > 0 {
		return engine + ":" + models[0].ID
	}
	if models := assistantEngineBuiltinModels(engine); len(models) > 0 {
		return engine + ":" + models[0].ID
	}
	return engine + ":" + assistantDefaultModel
}

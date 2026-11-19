package httpapi

import (
	"net/http"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

// executorModelInfo describes one selectable model of a built-in CLI.
type executorModelInfo struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Family  string                 `json:"family"`
	Credits string                 `json:"credits,omitempty"`
	Efforts []executorEffortOption `json:"efforts"`
}

// executorEffortOption is one reasoning-effort level of a built-in CLI.
type executorEffortOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// executorModelCatalog is the per-CLI model and effort catalog used by the
// agent wizard. CustomModel marks CLIs that accept free-form model IDs
// (custom base URLs); Credits is only surfaced for CodeBuddy.
type executorModelCatalog struct {
	Source      string                 `json:"source,omitempty"`
	Default     string                 `json:"default,omitempty"`
	CustomModel bool                   `json:"customModel,omitempty"`
	Models      []executorModelInfo    `json:"models"`
	Efforts     []executorEffortOption `json:"efforts"`
}

type builtinExecutorModel struct {
	id      string
	name    string
	efforts []string
}

// Reasoning levels offered by the built-in models on each CLI.
var builtinExecutorModels = func() map[string][]builtinExecutorModel {
	legacy := []string{"low", "medium", "high"}
	lowHighMax := []string{"low", "high", "max"}
	offLowHighMax := []string{"none", "low", "high", "max"}
	xhigh := []string{"low", "medium", "high", "xhigh"}
	max := []string{"low", "medium", "high", "xhigh", "max"}
	maxWithoutXHigh := []string{"low", "medium", "high", "max"}
	ultra := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	return map[string][]builtinExecutorModel{
		assistantEngineCodex: {
			{"gpt-6.1-sol", "GPT-6.1 Sol", ultra},
			{"gpt-6.1-terra", "GPT-6.1 Terra", ultra},
			{"gpt-6.2-sol", "GPT-6.2 Sol", ultra},
			{"gpt-6.2-astra", "GPT-6.2 Astra", ultra},
			{"gpt-6-astra", "GPT-6 Astra", ultra},
			{"gpt-6-sol", "GPT-6 Sol", ultra},
			{"gpt-6-luna", "GPT-6 Luna", max},
			{"gpt-5.6-sol", "GPT-5.6 Sol", ultra},
			{"gpt-5.6-terra", "GPT-5.6 Terra", ultra},
			{"gpt-5.6-luna", "GPT-5.6 Luna", max},
			{"gpt-5.5", "GPT-5.5", xhigh},
		},
		assistantEngineClaude: {
			{"claude-fable-5-1", "Claude Fable 5.1", max},
			{"claude-fable-5-2", "Claude Fable 5.2", max},
			{"claude-fable-5", "Claude Fable 5", max},
			{"claude-opus-5-5", "Claude Opus 5.5", max},
			{"claude-opus-5", "Claude Opus 5", max},
			{"claude-opus-4-8", "Claude Opus 4.8", max},
			{"claude-opus-4-7", "Claude Opus 4.7", max},
			{"claude-opus-4-6", "Claude Opus 4.6", maxWithoutXHigh},
			{"claude-sonnet-5-5", "Claude Sonnet 5.5", max},
			{"claude-sonnet-5", "Claude Sonnet 5", max},
			{"claude-sonnet-4-6", "Claude Sonnet 4.6", maxWithoutXHigh},
			// Keep accepting effort values saved in existing 4.5 conversations.
			{"claude-sonnet-4-5", "Claude Sonnet 4.5", legacy},
			{"claude-haiku-5-5", "Claude Haiku 5.5", max},
			{"claude-haiku-4-5", "Claude Haiku 4.5", legacy},
		},
		runtime.AgentCLIKimi: {
			{"k3.1-pro", "K3.1 Pro", lowHighMax},
			{"k3.1-pro-highspeed", "K3.1 Pro Highspeed", lowHighMax},
			{"k3", "K3", offLowHighMax},
			{"k3-256k", "K3 256K", offLowHighMax},
			{"kimi-for-coding", "K2.8 Preview", offLowHighMax},
			{"kimi-for-coding-highspeed", "K2.7 Code Highspeed", []string{"none", "thinking"}},
		},
		runtime.AgentCLIDeepSeek: {
			{"deepseek-flash", "DeepSeek V4.1 Flash", domain.ExecutorEffortLevels[runtime.AgentCLIDeepSeek]},
			{"deepseek-v4-pro", "DeepSeek V4 Pro", domain.ExecutorEffortLevels[runtime.AgentCLIDeepSeek]},
		},
		runtime.AgentCLIZCode: {
			{"glm-5.3", "GLM-5.3", lowHighMax},
			{"glm-5.3-flash", "GLM-5.3 Flash", lowHighMax},
		},
	}
}()

var effortOptionLabels = map[string]string{
	"none":      "关闭 none",
	"thinking":  "开启 thinking",
	"minimal":   "最低 minimal",
	"low":       "低 low",
	"medium":    "中 medium",
	"high":      "高 high",
	"xhigh":     "超高 xhigh",
	"max":       "最大 max",
	"ultra":     "极致 ultra",
	"ultracode": "极致 ultracode",
}

func effortOptions(levels []string) []executorEffortOption {
	options := make([]executorEffortOption, 0, len(levels))
	for _, level := range levels {
		label, ok := effortOptionLabels[level]
		if !ok {
			label = level
		}
		options = append(options, executorEffortOption{Value: level, Label: label})
	}
	return options
}

// executorModelCatalogs returns the fixed per-CLI catalog. The CodeBuddy list
// reuses the assistant catalog so the wizard, AI 助手 and 澄清 stay in sync.
func executorModelCatalogs(kimiBaseURL string) map[string]executorModelCatalog {
	codebuddyModels := make([]executorModelInfo, 0)
	known := assistantKnownCatalog()
	for _, model := range selectableCodeBuddyModels(known.Models) {
		codebuddyModels = append(codebuddyModels, executorModelInfo{ID: model.ID, Name: model.Name, Credits: model.Credits})
	}
	catalogs := map[string]executorModelCatalog{
		"codebuddy": {
			Default: known.Default,
			Models:  codebuddyModels,
			Efforts: effortOptions(domain.ExecutorEffortLevels["codebuddy"]),
		},
		"codex": {
			CustomModel: true,
			Efforts:     effortOptions(domain.ExecutorEffortLevels["codex"]),
		},
		"claude": {
			CustomModel: true,
			Efforts:     effortOptions(domain.ExecutorEffortLevels["claude-code"]),
		},
		// Official-endpoint catalogs. OpenCode and Pi have none, so their
		// models come from the Base URL.
		"opencode": {CustomModel: true, Efforts: effortOptions(domain.ExecutorEffortLevels["opencode"])},
		"cursor": {
			CustomModel: true,
			Models:      cursorCatalogModels(),
			Efforts:     effortOptions(domain.ExecutorEffortLevels["cursor"]),
		},
		"agy": {
			CustomModel: true,
			// Slugs from agy models in Gemini API mode. Account-login models
			// are queried from the user's runner because they vary by plan.
			Models: []executorModelInfo{
				{ID: "gemini-3.8-flash-medium", Name: "Gemini 3.8 Flash"},
				{ID: "gemini-4-neon-high", Name: "Gemini 4 Neon"},
				{ID: "gemini-4-carbon-medium", Name: "Gemini 4 Carbon"},
				{ID: "gemini-4-argon-medium", Name: "Gemini 4 Argon"},
				{ID: "gemini-4-hydrogen-high", Name: "Gemini 4 Hydrogen"},
				{ID: "gemini-3.7-flash-medium", Name: "Gemini 3.7 Flash"},
				{ID: "gemini-3.6-flash-medium", Name: "Gemini 3.6 Flash"},
				{ID: "gemini-3.1-pro-high", Name: "Gemini 3.1 Pro"},
			},
			Efforts: effortOptions(domain.ExecutorEffortLevels["agy"]),
		},
		"kimi":  {CustomModel: true, Efforts: effortOptions(domain.ExecutorEffortLevels["kimi"])},
		"pi":    {CustomModel: true, Efforts: effortOptions(domain.ExecutorEffortLevels["pi"])},
		"dsh":   {CustomModel: true, Efforts: effortOptions(domain.ExecutorEffortLevels["dsh"])},
		"zcode": {CustomModel: true, Efforts: effortOptions(domain.ExecutorEffortLevels["zcode"])},
	}
	for engine, catalog := range catalogs {
		modelEngine := engine
		if engine == "claude" {
			modelEngine = assistantEngineClaude
		}
		if engine == runtime.AgentCLIKimi && kimiBaseURL != "" {
			catalog.Efforts = effortOptions(nil)
		}
		for _, model := range builtinExecutorModels[modelEngine] {
			catalog.Models = append(catalog.Models, executorModelInfo{ID: model.id, Name: model.name})
		}
		for i := range catalog.Models {
			id := catalog.Models[i].ID
			catalog.Models[i].Family = assistantModelFamily(id, modelEngine)
			if modelEngine != assistantEngineCodeBuddy {
				id = modelEngine + ":" + id
			}
			catalog.Models[i].Efforts = effortOptions(assistantModelEffortsForBaseURL(id, nil, kimiBaseURL))
		}
		catalogs[engine] = catalog
	}
	return catalogs
}

func cursorCatalogModels() []executorModelInfo {
	var out []executorModelInfo
	for _, model := range runtime.CursorModels() {
		out = append(out, executorModelInfo{ID: model.ID, Name: model.Name})
	}
	return out
}

// executorModels returns built-in defaults and effort levels, with the
// signed-in editor's model lists substituted where available.
func (h *catalogHandlers) executorModels(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if c.Query("builtin") == "1" {
		c.JSON(http.StatusOK, executorModelCatalogs(""))
		return
	}
	var user domain.User
	if current, ok := h.requestUser(c.Request, true); ok {
		if stored, found := h.repo.User(current.ID); found {
			user = stored
			refreshCLIModels(c.Request.Context(), user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey)
		}
	}
	_, kimiBaseURL := assistantEngineCredential(user, runtime.AgentCLIKimi)
	catalogs := executorModelCatalogs(kimiBaseURL)
	if antigravityUsesCustomEndpoint(user) {
		catalog := catalogs[runtime.AgentCLIAntigravity]
		catalog.Models = []executorModelInfo{}
		catalogs[runtime.AgentCLIAntigravity] = catalog
	}
	for _, engine := range append([]string{assistantEngineCodeBuddy}, assistantCLIEngines()...) {
		models, _ := assistantDiscoveredModelsFor(c.Request.Context(), user, engine)
		if len(models) == 0 {
			continue
		}
		id := engine
		if engine == assistantEngineClaude {
			id = "claude"
		}
		catalog := catalogs[id]
		catalog.Source = "personal"
		catalog.Models = make([]executorModelInfo, 0, len(models))
		personal := cliModelsOf(models)
		for _, model := range models {
			if model.ID == assistantDefaultModel || (engine == assistantEngineCodeBuddy && codeBuddyIsDefaultModel(model.ID)) {
				continue
			}
			name := strings.TrimSpace(model.Name)
			if name == "" {
				name = model.ID
			}
			modelID := model.ID
			if engine != assistantEngineCodeBuddy {
				modelID = engine + ":" + modelID
			}
			catalog.Models = append(catalog.Models, executorModelInfo{
				ID:      model.ID,
				Name:    name,
				Family:  assistantModelFamily(model.ID, engine),
				Efforts: effortOptions(assistantModelEffortsForBaseURL(modelID, personal, kimiBaseURL)),
			})
		}
		if len(catalog.Models) > 0 {
			catalogs[id] = catalog
		}
	}
	c.JSON(http.StatusOK, catalogs)
}

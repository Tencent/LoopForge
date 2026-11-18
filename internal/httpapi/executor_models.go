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

var effortOptionLabels = map[string]string{
	"none":      "关闭 none",
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
func executorModelCatalogs() map[string]executorModelCatalog {
	codebuddyModels := make([]executorModelInfo, 0)
	known := assistantKnownCatalog()
	for _, model := range known.Models {
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
			Models: []executorModelInfo{
				{ID: "gpt-6.1-sol", Name: "GPT-6.1 Sol"},
				{ID: "gpt-6.1-terra", Name: "GPT-6.1 Terra"},
				{ID: "gpt-6.2-sol", Name: "GPT-6.2 Sol"},
				{ID: "gpt-6.2-astra", Name: "GPT-6.2 Astra"},
				{ID: "gpt-6-astra", Name: "GPT-6 Astra"},
				{ID: "gpt-6-sol", Name: "GPT-6 Sol"},
				{ID: "gpt-6-luna", Name: "GPT-6 Luna"},
				{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol"},
				{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra"},
				{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna"},
				{ID: "gpt-5.5", Name: "GPT-5.5"},
			},
			Efforts: effortOptions(domain.ExecutorEffortLevels["codex"]),
		},
		"claude": {
			CustomModel: true,
			Models: []executorModelInfo{
				{ID: "claude-fable-5-1", Name: "Claude Fable 5.1"},
				{ID: "claude-fable-5-2", Name: "Claude Fable 5.2"},
				{ID: "claude-fable-5", Name: "Claude Fable 5"},
				{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"},
				{ID: "claude-opus-5", Name: "Claude Opus 5"},
				{ID: "claude-opus-4-8", Name: "Claude Opus 4.8"},
				{ID: "claude-opus-4-7", Name: "Claude Opus 4.7"},
				{ID: "claude-opus-4-6", Name: "Claude Opus 4.6"},
				{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5"},
				{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
				{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6"},
				{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5"},
				{ID: "claude-haiku-5-5", Name: "Claude Haiku 5.5"},
				{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5"},
			},
			Efforts: effortOptions(domain.ExecutorEffortLevels["claude-code"]),
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
			Models: []executorModelInfo{
				{ID: "gemini-3.8-flash-medium", Name: "Gemini 3.8 Flash"},
				{ID: "gemini-4-argon-medium", Name: "Gemini 4 Argon"},
			},
			Efforts: effortOptions(domain.ExecutorEffortLevels["agy"]),
		},
		"kimi": {
			CustomModel: true,
			Models: []executorModelInfo{
				{ID: "k3.1-pro", Name: "K3.1 Pro"},
				{ID: "k3.1-pro-highspeed", Name: "K3.1 Pro Highspeed"},
				{ID: "k3", Name: "K3"},
				{ID: "k3-256k", Name: "K3 256K"},
				{ID: "kimi-for-coding", Name: "K2.8 Preview"},
				{ID: "kimi-for-coding-highspeed", Name: "K2.7 Code Highspeed"},
			},
		},
		"pi": {CustomModel: true, Efforts: effortOptions(domain.ExecutorEffortLevels["pi"])},
		"dsh": {
			CustomModel: true,
			Models: []executorModelInfo{
				{ID: "deepseek-flash", Name: "DeepSeek V4.1 Flash"},
				{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro"},
			},
		},
		"zcode": {
			CustomModel: true,
			Models: []executorModelInfo{
				{ID: "glm-5.3", Name: "GLM-5.3"},
				{ID: "glm-5.3-flash", Name: "GLM-5.3 Flash"},
			},
		},
	}
	for engine, catalog := range catalogs {
		for i := range catalog.Models {
			id := catalog.Models[i].ID
			if engine == "claude" {
				id = assistantEngineClaude + ":" + id
			} else if engine != assistantEngineCodeBuddy {
				id = engine + ":" + id
			}
			catalog.Models[i].Efforts = effortOptions(assistantModelEffortsIn(id, nil))
		}
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
		c.JSON(http.StatusOK, executorModelCatalogs())
		return
	}
	var user domain.User
	if current, ok := h.requestUser(c.Request, true); ok {
		if stored, found := h.repo.User(current.ID); found {
			user = stored
			refreshCLIModels(c.Request.Context(), user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey)
		}
	}
	catalogs := executorModelCatalogs()
	for _, engine := range append([]string{assistantEngineCodeBuddy}, assistantCLIEngines()...) {
		models := assistantDiscoveredModelsFor(c.Request.Context(), user, engine)
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
			name := strings.TrimSpace(model.Name)
			if name == "" {
				name = model.ID
			}
			modelID := model.ID
			if engine != assistantEngineCodeBuddy {
				modelID = engine + ":" + modelID
			}
			catalog.Models = append(catalog.Models, executorModelInfo{ID: model.ID, Name: name, Efforts: effortOptions(assistantModelEffortsIn(modelID, personal))})
		}
		catalogs[id] = catalog
	}
	c.JSON(http.StatusOK, catalogs)
}

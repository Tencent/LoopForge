package httpapi

import (
	"net/http"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// executorModelInfo describes one selectable model of a built-in CLI.
type executorModelInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Credits string `json:"credits,omitempty"`
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
	Default     string                 `json:"default,omitempty"`
	CustomModel bool                   `json:"customModel,omitempty"`
	Models      []executorModelInfo    `json:"models"`
	Efforts     []executorEffortOption `json:"efforts"`
}

var effortOptionLabels = map[string]string{
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
	return map[string]executorModelCatalog{
		"codebuddy": {
			Default: known.Default,
			Models:  codebuddyModels,
			Efforts: effortOptions(domain.ExecutorEffortLevels["codebuddy"]),
		},
		"codex": {
			CustomModel: true,
			Models: []executorModelInfo{
				{ID: "gpt-6.1-sol", Name: "GPT-6.1 Sol"},
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
				{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5"},
			},
			Efforts: effortOptions(domain.ExecutorEffortLevels["claude-code"]),
		},
	}
}

// executorModels serves the built-in model/effort catalog. It is a static,
// non-sensitive list (model IDs, credit multipliers, effort levels), so it is
// readable without login; role editing and runs still enforce auth elsewhere.
func (h *catalogHandlers) executorModels(c *gin.Context) {
	if current, ok := h.requestUser(c.Request, true); ok {
		if user, ok := h.repo.User(current.ID); ok {
			refreshCLIModels(c.Request.Context(), user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey)
		}
	}
	c.JSON(http.StatusOK, executorModelCatalogs())
}

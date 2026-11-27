package httpapi

import (
	"context"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// codexCatalogModels lists the models the Codex catalog describes: the
// personal catalog first, then the gateway's published models. The model of
// the current turn is always included, even when the gateway does not publish
// it, because Codex would fall back for it otherwise.
//
// Codex falls back to built-in metadata for a model it does not know, and
// that metadata understates the context window of a model served by another
// gateway. The catalog gives the CLI the real numbers.
func codexCatalogModels(ctx context.Context, user domain.User, model string) []runtime.CodexModel {
	seen := map[string]bool{}
	var out []runtime.CodexModel
	add := func(id, name string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, runtime.CodexModel{
			ID:     id,
			Name:   strings.TrimSpace(name),
			Efforts: assistantModelEffortsForBaseURL(id, user.CodexModels, ""),
		})
	}
	for _, personal := range user.CodexModels {
		add(personal.ID, personal.Name)
	}
	// The gateway's own list is the source of truth for what it serves.
	if baseURL := strings.TrimSpace(user.CodexBaseURL); baseURL != "" && strings.TrimSpace(user.CodexAPIKey) != "" {
		for _, discovered := range cachedAssistantEngineDiscovery(ctx, assistantEngineProtocol(user, assistantEngineCodex), user.CodexAPIKey, baseURL) {
			add(discovered.ID, discovered.Name)
		}
	}
	if engine, id := assistantModelEngine(model); engine == assistantEngineCodex {
		add(id, id)
	}
	return out
}

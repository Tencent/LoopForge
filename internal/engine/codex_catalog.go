package engine

import (
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// codexCatalogModels lists the models a Codex run may use, so that Codex
// does not fall back to its built-in metadata for a model it has never heard
// of. The fallback understates the context window of a model served by
// another gateway.
//
// The personal catalog is the source of truth: it is what the user chose in
// 个人配置. A run without one keeps Codex's own catalog.
func (e *Engine) codexCatalogModels(user domain.User) []runtime.CodexModel {
	if len(user.CodexModels) == 0 {
		return nil
	}
	out := make([]runtime.CodexModel, 0, len(user.CodexModels))
	for _, model := range user.CodexModels {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		out = append(out, runtime.CodexModel{ID: id, Name: strings.TrimSpace(model.Name)})
	}
	return out
}

package engine

import (
	"testing"

	"agentflow/internal/domain"
)

func TestCodexCatalogModelsUsesThePersonalCatalog(t *testing.T) {
	e := &Engine{}
	user := domain.User{CodexModels: []domain.CLIModel{
		{ID: "  domestic-model  ", Name: "  Domestic Model  "},
		{ID: "", Name: "no id"},
	}}
	got := e.codexCatalogModels(user)
	if len(got) != 1 {
		t.Fatalf("models = %+v", got)
	}
	if got[0].ID != "domestic-model" || got[0].Name != "Domestic Model" {
		t.Fatalf("model = %+v", got[0])
	}
}

func TestCodexCatalogModelsWithoutAPersonalCatalog(t *testing.T) {
	e := &Engine{}
	if got := e.codexCatalogModels(domain.User{}); got != nil {
		t.Fatalf("a user without a catalog keeps the CLI one: %+v", got)
	}
}

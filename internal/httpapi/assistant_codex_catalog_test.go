package httpapi

import (
	"context"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestCodexCatalogModelsCombinesThePersonalAndThePublishedCatalog(t *testing.T) {
	restore := assistantEngineDiscover
	assistantEngineDiscover = func(ctx context.Context, anthropic bool, apiKey, baseURL string) ([]assistantDiscoveredModel, error) {
		return []assistantDiscoveredModel{
			{ID: "published-model", Name: "Published Model"},
			{ID: "domestic-model", Name: "Published Domestic"},
		}, nil
	}
	t.Cleanup(func() { assistantEngineDiscover = restore })

	user := domain.User{
		CodexAPIKey:  "turn-key",
		CodexBaseURL: "https://gateway.example/v1",
		CodexModels:  []domain.CLIModel{{ID: "domestic-model", Name: "Domestic Model"}},
	}
	got := codexCatalogModels(context.Background(), user, "")
	if len(got) != 2 {
		t.Fatalf("models = %+v", got)
	}
	// The personal catalog comes first: it is what the user chose.
	if got[0].ID != "domestic-model" || got[0].Name != "Domestic Model" {
		t.Fatalf("the personal catalog must come first: %+v", got[0])
	}
	if got[1].ID != "published-model" {
		t.Fatalf("models = %+v", got)
	}
}

func TestCodexCatalogModelsIncludesTheModelOfTheTurn(t *testing.T) {
	restore := assistantEngineDiscover
	assistantEngineDiscover = func(ctx context.Context, anthropic bool, apiKey, baseURL string) ([]assistantDiscoveredModel, error) {
		return nil, nil
	}
	t.Cleanup(func() { assistantEngineDiscover = restore })
	// The discovery cache is shared by the process, so the entry of an earlier
	// test would answer this one.
	assistantDiscoveryCache.Delete(assistantDiscoveryKey("", "turn-key", "https://gateway.example/v1"))

	user := domain.User{CodexAPIKey: "turn-key", CodexBaseURL: "https://gateway.example/v1"}
	// The gateway does not publish this model, but the turn runs on it, so the
	// catalog has to describe it or Codex falls back for it.
	got := codexCatalogModels(context.Background(), user, "codex:turn-model")
	if len(got) != 1 {
		t.Fatalf("models = %+v", got)
	}
	if got[0].ID != "turn-model" {
		t.Fatalf("model = %+v", got[0])
	}
}

func TestCodexCatalogModelsWithoutAGateway(t *testing.T) {
	// A user on the official endpoint keeps the CLI's own catalog.
	user := domain.User{CodexModels: []domain.CLIModel{{ID: "personal-model"}}}
	got := codexCatalogModels(context.Background(), user, "")
	if len(got) != 1 || got[0].ID != "personal-model" {
		t.Fatalf("models = %+v", got)
	}
}

func TestCodexCatalogModelsSkipsAnEmptyID(t *testing.T) {
	user := domain.User{CodexModels: []domain.CLIModel{{ID: "   ", Name: "blank"}}}
	if got := codexCatalogModels(context.Background(), user, ""); len(got) != 0 {
		t.Fatalf("a model without an ID must be skipped: %+v", got)
	}
}

var _ = runtime.CodexModel{}

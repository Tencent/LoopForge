package httpapi

import (
	"context"
	"slices"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestAssistantEngineModelsKeepPersonalCatalogOnOfficialKey(t *testing.T) {
	previous := assistantEngineDiscover
	t.Cleanup(func() { assistantEngineDiscover = previous })
	var discovered []string
	assistantEngineDiscover = func(_ context.Context, _ bool, _, baseURL string) ([]assistantDiscoveredModel, error) {
		discovered = append(discovered, baseURL)
		return []assistantDiscoveredModel{{ID: "remote-model", Name: "Remote"}}, nil
	}
	ids := func(user domain.User) []string {
		var out []string
		for _, model := range assistantEngineModels(context.Background(), user) {
			out = append(out, model.ID)
		}
		return out
	}

	official := domain.User{CodexAPIKey: "codex-key", CodexModels: []domain.CLIModel{{ID: "gpt-5.5", Name: "GPT-5.5"}}}
	if got := ids(official); !slices.Equal(got, []string{"codex:gpt-5.5"}) || len(discovered) != 0 {
		t.Fatalf("official catalog: %v discovered %v", got, discovered)
	}

	builtin := ids(domain.User{CodexAPIKey: "codex-key", ClaudeCodeAPIKey: "claude-key"})
	if !slices.Contains(builtin, "codex:gpt-6.1-terra") || !slices.Contains(builtin, "claude-code:claude-haiku-5-5") {
		t.Fatalf("builtin catalog: %v", builtin)
	}
	if len(discovered) != 0 {
		t.Fatalf("official endpoint discovered models: %v", discovered)
	}

	gemini := domain.User{AgentCLICredentials: map[string]domain.AgentCLICredential{
		runtime.AgentCLIAntigravity: {APIKey: "gemini-key", BaseURL: "https://generativelanguage.googleapis.com", Protocol: domain.AgentCLIProtocolGemini, Models: []domain.CLIModel{{ID: "gemini-custom"}}},
	}}
	if got := ids(gemini); !slices.Equal(got, []string{"agy:gemini-custom"}) || len(discovered) != 0 {
		t.Fatalf("gemini catalog: %v discovered %v", got, discovered)
	}

	remote := domain.User{CodexAPIKey: "codex-key", CodexBaseURL: "https://catalog-test.example/v1"}
	if got := ids(remote); !slices.Equal(got, []string{"codex:remote-model"}) || len(discovered) != 1 {
		t.Fatalf("base url catalog: %v discovered %v", got, discovered)
	}
}

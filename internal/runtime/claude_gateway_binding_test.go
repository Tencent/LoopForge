package runtime

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// Issue #1130: Claude Code against a Bearer-only Anthropic gateway must
// authenticate the conversation workspace exactly like the local phase,
// which means ANTHROPIC_AUTH_TOKEN (Authorization: Bearer) must be bound
// whenever a custom Base URL is configured. The official endpoint keeps
// its x-api-key-only behavior.
func TestBindClaudeCodeSetsBearerForCustomGateway(t *testing.T) {
	creds := RunCredentials{ClaudeCode: ProviderCredential{APIKey: "claude-user-secret", BaseURL: "https://gateway.example/anthropic"}}
	command, err := providerForKind("claude-code").Prepare(providerInvocation{Executor: domain.ExecutorDefinition{Binary: "claude-code"}, Prompt: "test", Driver: "local"})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	bound, err := bindCommand(t.Context(), &localSandbox{workspace: workspace}, workspace, command, creds)
	if err != nil {
		t.Fatal(err)
	}
	if bound.environment["ANTHROPIC_API_KEY"] != "claude-user-secret" {
		t.Fatal("x-api-key binding lost")
	}
	if bound.environment["ANTHROPIC_AUTH_TOKEN"] != "claude-user-secret" {
		t.Fatal("Bearer-only gateway gets no ANTHROPIC_AUTH_TOKEN")
	}
	if bound.environment["ANTHROPIC_BASE_URL"] != "https://gateway.example/anthropic" {
		t.Fatal("custom endpoint lost")
	}
}

func TestBindClaudeCodeKeepsOfficialEndpointKeyOnly(t *testing.T) {
	creds := RunCredentials{ClaudeCode: ProviderCredential{APIKey: "claude-user-secret"}}
	command, err := providerForKind("claude-code").Prepare(providerInvocation{Executor: domain.ExecutorDefinition{Binary: "claude-code"}, Prompt: "test", Driver: "local"})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	bound, err := bindCommand(t.Context(), &localSandbox{workspace: workspace}, workspace, command, creds)
	if err != nil {
		t.Fatal(err)
	}
	if bound.environment["ANTHROPIC_AUTH_TOKEN"] != "" {
		t.Fatal("official endpoint must stay x-api-key only")
	}
	if !strings.HasPrefix(bound.environment["ANTHROPIC_BASE_URL"], "https://api.anthropic.com") {
		t.Fatal("official endpoint default lost")
	}
}

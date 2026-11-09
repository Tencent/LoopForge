package runtime

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestDeepSeekUsesCodexToolsWithSeparateCredentialBinding(t *testing.T) {
	cmd, err := PrepareProviderCommand(domain.ExecutorDefinition{Kind: "deepseek", Binary: "codex", Args: []string{"exec", "--json"}, Model: "deepseek-flash"}, "test prompt", "local", InvocationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Provider != "deepseek" || !cmd.PrivateHome || !hasArg(cmd.Argv, "--json") || !hasArg(cmd.Argv, "--ignore-user-config") {
		t.Fatal("DeepSeek did not preserve the isolated Codex tool runtime")
	}
	bindings := ""
	for _, binding := range cmd.CredentialBindings {
		bindings += string(binding) + ","
	}
	if !strings.Contains(bindings, "deepseek,") || strings.Contains(bindings, "claude-code,") || strings.Contains(bindings, "codebuddy,") {
		t.Fatal("DeepSeek did not select its own credential binding")
	}
}

func TestDeepSeekCredentialIsolationAndDefaultEndpoint(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "host-claude-key")
	t.Setenv("DEEPSEEK_API_KEY", "host-deepseek-key")
	workspace := t.TempDir()
	sandbox := &localSandbox{workspace: workspace}
	command, err := PrepareProviderCommand(domain.ExecutorDefinition{Kind: "deepseek", Binary: "codex"}, "test prompt", "local", InvocationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	credentials := RunCredentials{DeepSeek: ProviderCredential{APIKey: "actor-deepseek-secret"}, ClaudeCode: ProviderCredential{APIKey: "other-claude-secret"}}
	bound, err := bindCommand(sandbox, workspace, command, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if bound.environment["CODEX_API_KEY"] != credentials.DeepSeek.APIKey || !strings.Contains(strings.Join(bound.Argv, " "), domain.DeepSeekDefaultBaseURL) || !strings.Contains(strings.Join(bound.Argv, " "), `wire_api="responses"`) || bound.environment["DEEPSEEK_API_KEY"] != "" {
		t.Fatal("DeepSeek did not bind only the responsible actor's credentials")
	}
	if strings.Contains(strings.Join(bound.Argv, " "), "secret") || !strings.Contains(bound.environment["CODEX_HOME"], "deepseek") {
		t.Fatal("DeepSeek credentials or CLI state are not isolated")
	}
	credentials.DeepSeek.BaseURL = "https://gateway.example/edge-b"
	bound, err = bindCommand(sandbox, workspace, command, credentials)
	if err != nil || !strings.Contains(strings.Join(bound.Argv, " "), credentials.DeepSeek.BaseURL) {
		t.Fatal("DeepSeek custom endpoint was not applied")
	}
	if _, err := bindCommand(sandbox, workspace, command, RunCredentials{ClaudeCode: credentials.ClaudeCode}); err == nil {
		t.Fatal("DeepSeek fell back to a different provider's key")
	}
}

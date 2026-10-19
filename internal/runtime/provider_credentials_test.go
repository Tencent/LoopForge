package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestProviderCredentialBindingIsolation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "host-openai")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "host-anthropic")
	t.Setenv("CODEBUDDY_API_KEY", "host-codebuddy")
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	creds := RunCredentials{CodeBuddy: CodeBuddyCredential{APIKey: "codebuddy-user-secret", InternetEnvironment: "ioa"}, Codex: ProviderCredential{APIKey: "codex-user-secret", BaseURL: "https://codex.example/v1"}, ClaudeCode: ProviderCredential{APIKey: "claude-user-secret", BaseURL: "https://claude.example"}}
	for _, kind := range []string{"codebuddy", "codex", "claude-code"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			sb := &localSandbox{workspace: workspace}
			command, err := providerForKind(kind).Prepare(providerInvocation{Executor: domain.ExecutorDefinition{Binary: kind}, Prompt: "test", Driver: "local"})
			if err != nil {
				t.Fatal(err)
			}
			bound, err := bindCommand(sb, workspace, command, creds)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(bound.Argv, " "), "user-secret") {
				t.Fatal("key leaked into command arguments")
			}
			if bound.environment["OPENAI_API_KEY"] != "" || bound.environment["ANTHROPIC_AUTH_TOKEN"] != "" {
				t.Fatal("host credentials not cleared")
			}
			home := bound.environment["HOME"]
			if !strings.HasPrefix(home, workspace+string(os.PathSeparator)) {
				t.Fatal("provider home not isolated")
			}
			if kind == "codebuddy" {
				if bound.environment["CODEBUDDY_API_KEY"] != "codebuddy-user-secret" || bound.environment["CODEBUDDY_INTERNET_ENVIRONMENT"] != "ioa" || bound.environment["CODEX_API_KEY"] != "" || bound.environment["ANTHROPIC_API_KEY"] != "" {
					t.Fatal("CodeBuddy credential routing failed")
				}
				bound.Argv = []string{"sh", "-c", `test "$CODEBUDDY_API_KEY" = codebuddy-user-secret && test "$CODEBUDDY_INTERNET_ENVIRONMENT" = ioa && test -z "$CODEX_API_KEY" && test -z "$ANTHROPIC_API_KEY" && printf ok`}
			} else if kind == "codex" {
				if bound.environment["CODEX_API_KEY"] != "codex-user-secret" || bound.environment["ANTHROPIC_API_KEY"] != "" || bound.environment["CODEX_HOME"] != filepath.Join(home, ".codex") {
					t.Fatal("Codex credential routing failed")
				}
				if !strings.Contains(strings.Join(bound.Argv, " "), "https://codex.example/v1") {
					t.Fatal("custom Codex endpoint missing")
				}
				bound.Argv = []string{"sh", "-c", `test "$CODEX_API_KEY" = codex-user-secret && test -z "$ANTHROPIC_API_KEY" && test -z "$OPENAI_API_KEY" && printf ok`}
			} else {
				if bound.environment["ANTHROPIC_API_KEY"] != "claude-user-secret" || bound.environment["ANTHROPIC_BASE_URL"] != "https://claude.example" || bound.environment["CODEX_API_KEY"] != "" || bound.environment["CLAUDE_CONFIG_DIR"] != filepath.Join(home, ".claude") {
					t.Fatal("Claude credential routing failed")
				}
				bound.Argv = []string{"sh", "-c", `test "$ANTHROPIC_API_KEY" = claude-user-secret && test -z "$CODEX_API_KEY" && test -z "$ANTHROPIC_AUTH_TOKEN" && printf ok`}
			}
			output, err := sb.Exec(context.Background(), bound, nil)
			if err != nil || strings.TrimSpace(output) != "ok" {
				t.Fatalf("subprocess environment failed: %v", err)
			}
			// Without a responsible user's key, no host credential may be reused.
			_, err = bindCommand(sb, workspace, command, RunCredentials{})
			if err == nil || !strings.Contains(err.Error(), "未配置") {
				t.Fatal("missing user key must fail before any CLI can reuse host login")
			}
		})
	}
	plain, err := bindCommand(&localSandbox{workspace: t.TempDir()}, t.TempDir(), CommandSpec{Argv: []string{"true"}}, creds)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"CODEBUDDY_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY"} {
		if plain.environment[key] != "" {
			t.Fatal("plain command received provider credentials")
		}
	}
	redacted := compactCommand([]string{"docker", "exec", "-e", "CODEX_API_KEY=codex-user-secret", "-e", "ANTHROPIC_API_KEY=claude-user-secret"})
	if strings.Contains(redacted, "user-secret") {
		t.Fatal("process telemetry leaked provider credentials")
	}
}

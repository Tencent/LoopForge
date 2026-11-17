package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConversationRunnerLoginUsesOnlyNativeAuthInIsolatedHome(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code"} {
		t.Run(provider, func(t *testing.T) {
			if provider == "codex" {
				if _, err := exec.LookPath("python3"); err != nil {
					t.Skip("python3 unavailable")
				}
			}
			login, _ := runnerLoginSpec(provider)
			host := t.TempDir()
			root := t.TempDir()
			t.Setenv(login.configEnv, host)
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
			t.Setenv("ANTHROPIC_API_KEY", "host-key")
			t.Setenv("OPENAI_API_KEY", "host-key")
			file := filepath.Join(host, login.file)
			if err := os.WriteFile(file, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(host, "settings.json"), []byte("host settings"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pool := &ConversationProcessPool{}
			script := `set -eu
test -f "$HOME/$1/$2"
test ! -e "$HOME/$1/settings.json"
test -z "${OPENAI_API_KEY:-}${CODEX_API_KEY:-}${ANTHROPIC_API_KEY:-}"
printf 'local-auth-ok\n'`
			req := ConversationProcessRequest{Operation: "start", Command: &ConversationCommand{
				Argv: []string{"sh", "-c", script, "runner-login", login.dir, login.file}, Provider: provider,
				Bindings: []CredentialBinding{login.binding}, RunnerLogin: true,
				Credentials: RunCredentials{Codex: ProviderCredential{APIKey: "personal"}, ClaudeCode: ProviderCredential{APIKey: "personal"}},
			}}
			started, err := pool.Handle(ctx, root, "", nil, req, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := conversationPoll(t, pool, ctx, root, started.ID); strings.TrimSpace(got) != "local-auth-ok" {
				t.Fatalf("isolated CLI did not see expected login state: %q", got)
			}
			if _, err := pool.Handle(ctx, root, "", nil, ConversationProcessRequest{Operation: "stop", ID: started.ID}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "home", login.dir, login.file)); !os.IsNotExist(err) {
				t.Fatalf("local credential copy was not removed: %v", err)
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != "{}" {
				t.Fatal("native CLI login file changed")
			}
			stale := filepath.Join(root, "home", login.dir, login.file)
			if err := os.WriteFile(stale, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			req.Command.RunnerLogin = false
			req.Command.Credentials = RunCredentials{}
			if _, err := pool.Handle(ctx, root, "", nil, req, nil); err == nil || !strings.Contains(err.Error(), "API Key") {
				t.Fatalf("personal mode fell back to runner login: %v", err)
			}
			if _, err := os.Stat(stale); !os.IsNotExist(err) {
				t.Fatal("personal mode retained a stale login file")
			}
			req.Command.RunnerLogin = true

			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Handle(ctx, root, "", nil, req, nil); err == nil || !strings.Contains(err.Error(), "本机登录态不可用") {
				t.Fatalf("missing native login did not report a runner-specific error: %v", err)
			}
		})
	}
}

func TestConversationRunnerLoginSupportsClaudeOAuthEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "runner-fixture")
	req := ConversationProcessRequest{Operation: "start", Command: &ConversationCommand{
		Provider: "claude-code", Bindings: []CredentialBinding{BindClaudeCode}, RunnerLogin: true,
		Argv: []string{"sh", "-c", `test "$CLAUDE_CODE_OAUTH_TOKEN" = runner-fixture && test ! -e "$CLAUDE_CONFIG_DIR/.credentials.json" && printf 'ok\n'`},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool := &ConversationProcessPool{}
	started, err := pool.Handle(ctx, root, "", nil, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := conversationPoll(t, pool, ctx, root, started.ID); strings.TrimSpace(got) != "ok" {
		t.Fatal("CLI did not use the Runner OAuth environment")
	}
}

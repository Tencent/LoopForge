package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A conversation workspace CLI does its own git: the requester's token is
// offered to github.com only, gh sees it as GH_TOKEN, and the requester's
// identity is a default the agent can still override.
func TestBindGitWorkspaceScopesTokenToGitHub(t *testing.T) {
	t.Setenv("GH_TOKEN", "host-token-must-not-leak")
	t.Setenv("GITHUB_TOKEN", "host-token-must-not-leak")
	// The identity is a system-level default; a developer's global config wins.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	workspace := t.TempDir()
	script := strings.Join([]string{
		`printf 'gh=%s\n' "$GH_TOKEN"`,
		`printf 'github=%s\n' "$("$GIT_ASKPASS" "Password for 'https://x-access-token@github.com': ")"`,
		`printf 'path=%s\n' "$("$GIT_ASKPASS" "Password for 'https://x-access-token@github.com/octo/widgets.git': ")"`,
		`printf 'port=%s\n' "$("$GIT_ASKPASS" "Password for 'https://x-access-token@github.com:443/octo/widgets.git': ")"`,
		`"$GIT_ASKPASS" "Password for 'https://x-access-token@github.com:444/octo/widgets.git': "; printf 'badport=%s\n' "$?"`,
		`printf 'user=%s\n' "$("$GIT_ASKPASS" "Username for 'https://github.com': ")"`,
		`"$GIT_ASKPASS" "Password for 'https://evil.example.org': "; printf 'evil=%s\n' "$?"`,
		`"$GIT_ASKPASS" "Password for 'https://x-access-token@github.com.evil.example': "; printf 'suffix=%s\n' "$?"`,
		`"$GIT_ASKPASS" "Username for 'https://github.com@evil.example': "; printf 'userinfo=%s\n' "$?"`,
		`"$GIT_ASKPASS" "Password for 'https://evil.example/github.com': "; printf 'inpath=%s\n' "$?"`,
		`"$GIT_ASKPASS" "Password for 'http://github.com': "; printf 'plain=%s\n' "$?"`,
		`printf 'prompt=%s\n' "$GIT_TERMINAL_PROMPT"`,
		`git config --get user.name; true`,
	}, "; ")
	run := func(credentials RunCredentials) string {
		t.Helper()
		out, err := ExecuteLocalCommand(context.Background(), workspace, CommandSpec{Argv: []string{"sh", "-c", script}, CredentialBindings: []CredentialBinding{BindGitWorkspace}}, credentials, nil)
		if err != nil {
			t.Fatalf("command: %v (%s)", err, out)
		}
		return out
	}
	out := run(RunCredentials{GitToken: "ghp_workspace", GitUserName: "alice", GitUserEmail: "alice@example.invalid"})
	for _, want := range []string{"gh=ghp_workspace", "github=ghp_workspace", "path=ghp_workspace", "port=ghp_workspace", "user=x-access-token", "badport=1", "evil=1", "suffix=1", "userinfo=1", "inpath=1", "plain=1", "prompt=0", "alice"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "ghp_workspace") != 4 {
		t.Fatalf("the token reached a host other than github.com:\n%s", out)
	}
	if strings.Contains(out, "host-token-must-not-leak") {
		t.Fatalf("the platform's own GitHub token reached the workspace:\n%s", out)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "runtime", "git-askpass-hosts")); err != nil || strings.Contains(string(data), "ghp_workspace") {
		t.Fatalf("askpass script must read the token from the environment: %v", err)
	}

	// A prefixed grant is not a GitHub token: nothing is offered.
	for _, token := range []string{"oauth2:other-grant", "private:other-token"} {
		out = run(RunCredentials{GitToken: token})
		if !strings.Contains(out, "gh=\n") || strings.Contains(out, "other-") {
			t.Fatalf("%s output:\n%s", token, out)
		}
	}
	// Without a token nothing answers git's prompt and gh has no token.
	out = run(RunCredentials{})
	if !strings.Contains(out, "gh=\n") {
		t.Fatalf("no token output:\n%s", out)
	}
}

func TestWorkspaceGitIdentityEscapesConfig(t *testing.T) {
	if got := workspaceGitIdentity("", ""); got != "" {
		t.Fatalf("empty identity = %q", got)
	}
	got := workspaceGitIdentity("a\"b\\c\n[core]", "x@y")
	if strings.Count(got, "\n") != 3 || strings.Contains(got, "\n[core]") || !strings.Contains(got, `email = "x@y"`) {
		t.Fatalf("identity = %q", got)
	}
}

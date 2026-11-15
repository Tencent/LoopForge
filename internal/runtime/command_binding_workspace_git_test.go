package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// A conversation workspace CLI does its own git: the requester's token is
// offered to GitHub and Gongfeng only, gh sees it as GH_TOKEN, and the
// requester's identity is a default the agent can still override.
func TestBindGitWorkspaceScopesTokenToKnownHosts(t *testing.T) {
	t.Setenv("GH_TOKEN", "host-token-must-not-leak")
	t.Setenv("GITHUB_TOKEN", "host-token-must-not-leak")
	workspace := t.TempDir()
	script := strings.Join([]string{
		`printf 'gh=%s\n' "$GH_TOKEN"`,
		`printf 'github=%s\n' "$("$GIT_ASKPASS" "Password for 'https://x-access-token@github.com': ")"`,
		`printf 'user=%s\n' "$("$GIT_ASKPASS" "Username for 'https://github.com': ")"`,
		`printf 'gongfeng=%s\n' "$("$GIT_ASKPASS" "Password for 'https://` + domain.GongfengRepoHost + `': ")"`,
		`"$GIT_ASKPASS" "Password for 'https://evil.example.org': "; printf 'evil=%s\n' "$?"`,
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
	for _, want := range []string{"gh=ghp_workspace", "github=ghp_workspace", "user=x-access-token", "gongfeng=ghp_workspace", "evil=1", "prompt=0", "alice"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "host-token-must-not-leak") {
		t.Fatalf("the platform's own GitHub token reached the workspace:\n%s", out)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "runtime", "git-askpass-hosts")); err != nil || strings.Contains(string(data), "ghp_workspace") {
		t.Fatalf("askpass script must read the token from the environment: %v", err)
	}

	// A Gongfeng OAuth grant authenticates as oauth2 and is not a GitHub token.
	out = run(RunCredentials{GitToken: "oauth2:gf-grant"})
	if !strings.Contains(out, "gongfeng=gf-grant") || !strings.Contains(out, "gh=\n") {
		t.Fatalf("oauth2 token output:\n%s", out)
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

package runtime

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A conversation workspace CLI does its own git: Git's credential helper and
// the gh wrapper fetch the token of the exact host from the session gateway,
// no token sits in the environment or the workspace, and the requester's
// identity is a default the agent can still override.
func TestBindGitWorkspaceFetchesTokenPerHost(t *testing.T) {
	t.Setenv("GH_TOKEN", "host-token-must-not-leak")
	t.Setenv("GITHUB_TOKEN", "host-token-must-not-leak")
	// The identity is a system-level default; a developer's global config wins.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	tools := t.TempDir()
	fakeGH := "#!/bin/sh\nprintf 'realgh token=%s enterprise=%s host=%s args=%s\\n' \"$GH_TOKEN\" \"$GH_ENTERPRISE_TOKEN\" \"$GH_HOST\" \"$*\"\n"
	if err := os.WriteFile(filepath.Join(tools, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))

	var mu sync.Mutex
	var asked []string
	tokens := map[string]string{"git.example.org": "glpat-workspace", "ghe.example.org": "ghe-workspace"}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params map[string]string `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Method != gitCredentialMethod {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		asked = append(asked, req.Params["caller"]+":"+req.Params["host"])
		mu.Unlock()
		result := map[string]any{}
		if token := tokens[req.Params["host"]]; token != "" && req.Params["protocol"] == "https" {
			result = map[string]any{"username": "oauth2", "password": token}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer gateway.Close()

	workspace := t.TempDir()
	script := strings.Join([]string{
		`printf 'gh=%s askpass=%s\n' "$GH_TOKEN" "$GIT_ASKPASS"`,
		`printf 'protocol=https\nhost=git.example.org\n\n' | git credential fill`,
		`printf 'protocol=https\nhost=evil.example.org\n\n' | git credential fill >/dev/null 2>&1; printf 'evil=%s\n' "$?"`,
		`printf 'hosts=%s\n' "$AGENTFLOW_GIT_HOSTS"`,
		`printf 'prompt=%s\n' "$GIT_TERMINAL_PROMPT"`,
		`GH_HOST=ghe.example.org gh api user`,
		`gh pr list`,
		`git config --get user.name; true`,
	}, "; ")
	run := func(env map[string]string, credentials RunCredentials) string {
		t.Helper()
		command := WithCommandEnvironment(CommandSpec{Argv: []string{"sh", "-c", script}, CredentialBindings: []CredentialBinding{BindGitWorkspace}}, env)
		out, err := ExecuteLocalCommand(context.Background(), workspace, command, credentials, nil)
		if err != nil {
			t.Fatalf("command: %v (%s)", err, out)
		}
		return out
	}
	hosts := map[string]string{"git.example.org": "gitlab", "ghe.example.org": "github", "bad host": "other"}
	out := run(map[string]string{SessionGatewayEnv: gateway.URL}, RunCredentials{GitHTTPHosts: hosts, GitUserName: "alice", GitUserEmail: "alice@example.invalid"})
	for _, want := range []string{
		"gh= askpass=\n",
		"username=oauth2\npassword=glpat-workspace\n",
		"evil=128",
		"hosts=ghe.example.org=github,git.example.org=gitlab\n",
		"prompt=0",
		"realgh token= enterprise=ghe-workspace host=ghe.example.org args=api user",
		"realgh token= enterprise= host= args=pr list",
		"alice",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "host-token-must-not-leak") {
		t.Fatalf("the platform's own GitHub token reached the workspace:\n%s", out)
	}
	mu.Lock()
	got := strings.Join(asked, ",")
	mu.Unlock()
	if got != "git:git.example.org,git:evil.example.org,gh:ghe.example.org,gh:github.com" {
		t.Fatalf("gateway requests = %s", got)
	}
	_ = filepath.WalkDir(filepath.Join(workspace, ".agentflow"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "agentflow-helper" {
			return err
		}
		if data, _ := os.ReadFile(path); strings.Contains(string(data), "-workspace") {
			t.Fatalf("%s holds a token", path)
		}
		return nil
	})

	// Without a session gateway or exposed hosts no helper is registered and
	// gh runs unwrapped.
	for _, tc := range []struct {
		env         map[string]string
		credentials RunCredentials
	}{
		{nil, RunCredentials{GitHTTPHosts: hosts}},
		{map[string]string{SessionGatewayEnv: gateway.URL}, RunCredentials{}},
	} {
		out = run(tc.env, tc.credentials)
		if strings.Contains(out, "password=") || !strings.Contains(out, "hosts=\n") || !strings.Contains(out, "enterprise= host=ghe.example.org args=api user") {
			t.Fatalf("unbound output:\n%s", out)
		}
	}
}

// gh and glab logins written in one turn are gone in the next.
func TestBindGitWorkspaceResetsCLIConfig(t *testing.T) {
	provideDockerHelperFixture(t)
	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace, driver: "docker"}
	env := map[string]string{SessionGatewayEnv: "http://relay.invalid/mcp"}
	if err := bindWorkspaceGitEnvironment(t.Context(), sb, workspace, env, RunCredentials{GitHTTPHosts: map[string]string{"github.com": "github"}}); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(workspace, ".agentflow", "runtime", "gh-config", "hosts.yml")
	if err := os.WriteFile(stale, []byte("oauth_token: stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	env = map[string]string{SessionGatewayEnv: "http://relay.invalid/mcp"}
	if err := bindWorkspaceGitEnvironment(t.Context(), sb, workspace, env, RunCredentials{GitHTTPHosts: map[string]string{"github.com": "github"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale gh login survived: %v", err)
	}
	if env["GH_CONFIG_DIR"] != "/workspace/.agentflow/runtime/gh-config" || env["GLAB_CONFIG_DIR"] != "/workspace/.agentflow/runtime/glab-config" {
		t.Fatalf("config dirs = %q %q", env["GH_CONFIG_DIR"], env["GLAB_CONFIG_DIR"])
	}
	if env["GIT_CONFIG_VALUE_1"] != "!'/workspace/.agentflow/runtime/bin/agentflow-helper' git-credential" || env["PATH"] != "" {
		t.Fatalf("docker helper = %q, PATH = %q", env["GIT_CONFIG_VALUE_1"], env["PATH"])
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

package runtime

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/gitwork"
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

func TestWorkspaceGitScopesHTTPRedirectsToAuthorizedOrigin(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	scope := "http://[::1]:" + port
	raw := "http://[0:0:0:0:0:0:0:1]:" + port
	const secret = "fixture-workspace-git-token"
	git := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ipv6 := strings.HasPrefix(r.Host, "[")
		switch r.URL.Path {
		case "/moved.git/info/refs":
			target := "/public.git/info/refs"
			if ipv6 {
				target = "/private.git/info/refs"
			}
			http.Redirect(w, r, target+"?"+r.URL.RawQuery, http.StatusMovedPermanently)
			return
		case "/private.git/info/refs":
			username, password, ok := r.BasicAuth()
			if !ipv6 || !ok || username != "git" || password != secret {
				w.Header().Set("WWW-Authenticate", `Basic realm="Git"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		case "/public.git/info/refs":
			if _, _, ok := r.BasicAuth(); ok {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("0123456789012345678901234567890123456789\trefs/heads/main\n"))
	}))
	git.Listener.Close()
	git.Listener = listener
	git.Start()
	t.Cleanup(git.Close)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			Method string            `json:"method"`
			Params map[string]string `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil || rpc.Method != gitCredentialMethod {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		result := gitCredentialReply{}
		host, valid := gitwork.GitCredentialScope(rpc.Params["protocol"], rpc.Params["host"])
		if valid && host == scope {
			result = gitCredentialReply{Username: "git", Password: secret}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(gateway.Close)
	workspace := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", workspace).CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture repository: %s: %v", out, err)
	}
	key := "http." + raw + "/moved.git.followRedirects"
	if out, err := exec.CommandContext(t.Context(), "git", "-C", workspace, "config", "--local", key, "true").CombinedOutput(); err != nil {
		t.Fatalf("set local redirect policy: %s: %v", out, err)
	}
	for _, mode := range []string{"shell", "direct", "login shell"} {
		t.Run(mode, func(t *testing.T) {
			run := func(remote string) (string, error) {
				argv := []string{"git", "ls-remote", "--", remote}
				switch mode {
				case "shell":
					argv = append([]string{"sh", "-c", `exec "$@"`, "sh"}, argv...)
				case "login shell":
					// Reproduce login profiles that discard the Runner's PATH additions.
					argv = append([]string{"sh", "-lc", `PATH="$1"; export PATH; shift; exec "$@"`, "sh", os.Getenv("PATH")}, argv...)
				}
				cmd := WithCommandEnvironment(CommandSpec{
					Argv:               argv,
					CredentialBindings: []CredentialBinding{BindGitWorkspace},
				}, map[string]string{SessionGatewayEnv: gateway.URL, "NO_PROXY": "*", "no_proxy": "*", "GIT_CONFIG_GLOBAL": os.DevNull})
				return ExecuteLocalCommand(t.Context(), workspace, cmd, RunCredentials{
					GitHTTPHosts: map[string]string{scope: "other"},
				}, nil)
			}
			for _, remote := range []string{
				raw + "/private.git",
				scope + "/private.git",
				"http://localhost:" + port + "/moved.git",
			} {
				if out, err := run(remote); err != nil || !strings.Contains(out, "refs/heads/main") {
					t.Fatalf("ls-remote %s: %v (%s)", remote, err, out)
				}
			}
			if out, err := run(raw + "/moved.git"); err == nil || !strings.Contains(out+" "+err.Error(), "301") {
				t.Fatalf("authorized IPv6 redirect should fail: %v (%s)", err, out)
			}
		})
	}
}

func TestWorkspaceGitPreservesSubmoduleScripts(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	workspace := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", workspace).CombinedOutput(); err != nil {
		t.Fatalf("initialize repository: %v (%s)", err, out)
	}
	cmd := WithCommandEnvironment(CommandSpec{
		Argv:               []string{"sh", "-c", "exec git submodule status"},
		CredentialBindings: []CredentialBinding{BindGitWorkspace},
	}, map[string]string{SessionGatewayEnv: "http://127.0.0.1:1"})
	credentials := RunCredentials{GitHTTPHosts: map[string]string{"http://git.example.org": "other"}}
	if out, err := ExecuteLocalCommand(t.Context(), workspace, cmd, credentials, nil); err != nil {
		t.Fatalf("git submodule status: %v (%s)", err, out)
	}
}

// gh and glab logins written in one turn are gone in the next.
func TestBindGitWorkspaceResetsCLIConfig(t *testing.T) {
	provideDockerHelperFixture(t)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	sb := &dockerSandbox{workspace: workspace}
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
	if env["GIT_CONFIG_VALUE_1"] != "!'"+dockerGitToolsMount+"/bin/agentflow-helper' git-credential" || env["PATH"] != "" {
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

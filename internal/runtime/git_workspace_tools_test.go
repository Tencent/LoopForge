package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

const gitHelperDockerTestImage = "golang:1.26.11-bookworm"

func gitHelperDockerTestRoot(t *testing.T) string {
	t.Helper()
	if os.Getenv("AGENTFLOW_TEST_GIT_HELPERS_DOCKER") != "1" {
		t.Skip("enable the disposable Docker Git helper test explicitly")
	}
	root, err := os.MkdirTemp(os.Getenv("AGENTFLOW_TEST_GIT_HELPERS_ROOT"), "shared-git-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", gitHelperDockerTestImage)
	return root
}

func TestSharedWorkspaceGitHelpersReadOnly(t *testing.T) {
	for _, driver := range []string{domain.SandboxDriverNative, domain.SandboxDriverDocker} {
		t.Run(driver, func(t *testing.T) {
			host, listen := "127.0.0.1", "127.0.0.1:0"
			var toolsRoot string
			resources := SandboxResources{}
			root := t.TempDir()
			if driver == domain.SandboxDriverDocker {
				root = gitHelperDockerTestRoot(t)
				host = os.Getenv("AGENTFLOW_TEST_GIT_HELPERS_HOST")
				if host == "" {
					t.Fatal("AGENTFLOW_TEST_GIT_HELPERS_HOST must reach this test process")
				}
				listen, toolsRoot = "0.0.0.0:0", dockerGitToolsMount
				resources.Dockerfile = domain.DockerfileDefinition{Content: IsolatedDockerfile}
			} else {
				if !NativeSandboxAvailable(driver) {
					t.Skip("native sandbox unavailable")
				}
				t.Setenv("HOME", filepath.Join(root, "home"))
				t.Setenv("XDG_CACHE_HOME", "")
				var err error
				toolsRoot, err = os.UserCacheDir()
				if err != nil {
					t.Fatal(err)
				}
			}
			testSharedWorkspaceGitHelpers(t, driver, root, host, listen, toolsRoot, resources)
		})
	}
}

func testSharedWorkspaceGitHelpers(t *testing.T, driver, root, host, listen, toolsRoot string, resources SandboxResources) {
	t.Helper()
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	authority := net.JoinHostPort(host, port)
	origin := "http://" + authority
	tokens := map[string]string{"alice": "alice-fixture-value", "bob": "bob-fixture-value"}
	var mu sync.Mutex
	var seen []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if speaker, ok := strings.CutPrefix(r.URL.Path, "/relay/"); ok {
			var call struct {
				Method string            `json:"method"`
				Params map[string]string `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&call); err != nil || call.Method != gitCredentialMethod {
				http.Error(w, "invalid test request", http.StatusBadRequest)
				return
			}
			reply := gitCredentialReply{}
			if token := tokens[speaker]; token != "" && call.Params["protocol"] == "http" && call.Params["host"] == authority {
				reply = gitCredentialReply{Username: speaker, Password: token}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": reply})
			return
		}
		if r.URL.Path != "/repo.git/info/refs" {
			http.NotFound(w, r)
			return
		}
		user, token, ok := r.BasicAuth()
		if !ok || tokens[user] == "" || tokens[user] != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="Git fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		seen = append(seen, user)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("0123456789012345678901234567890123456789\trefs/heads/main\n"))
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	workspace := filepath.Join(root, "workspace")
	sb, err := OpenRemoteSandbox(t.Context(), "shared-git-"+filepath.Base(root), workspace, domain.SandboxProfile{Driver: driver}, SandboxRequirements{}, resources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := sb.Close(ctx); err != nil {
			t.Errorf("close shared workspace: %v", err)
		}
	})
	if out, err := sb.Exec(t.Context(), CommandSpec{Argv: []string{"sh", "-ec", `git init -q && printf ready > shared-marker && printf live > /tmp/shared-git-state`}}, nil); err != nil {
		t.Fatalf("start shared workspace: %v (%s)", err, out)
	}
	var dockerIdentity string
	if driver == domain.SandboxDriverDocker {
		dockerIdentity, err = ReadSandboxEnvironmentIdentity(t.Context(), sb)
		if err != nil {
			t.Fatal(err)
		}
		mounts, err := inspectDockerWorkspace(t.Context(), sb.(*dockerSandbox).name)
		if err != nil || mounts == nil || mounts.gitTools == "" {
			t.Fatalf("a newly created Docker workspace must already have its read-only Git mount: %+v, %v", mounts, err)
		}
	}
	const readOnlyProbe = `test "$(cat shared-marker)" = ready
if [ "$3" = docker ]; then test "$(cat /tmp/shared-git-state)" = live; fi
git_exec=$(git --exec-path)
case "$git_exec" in "$1"/*) ;; *) exit 11 ;; esac
if touch "$git_exec/workspace-write-probe" 2>/dev/null; then exit 12; fi
if touch "${AGENTFLOW_WORKSPACE_GIT_BINS%%:*}/workspace-write-probe" 2>/dev/null; then exit 13; fi
git submodule status
if [ "$3" = docker ]; then
  exec sh -lc 'PATH=/usr/bin:/bin; export PATH; exec git ls-remote -- "$1"' git-login "$2"
fi
exec git ls-remote -- "$2"`
	for _, speaker := range []string{"alice", "bob", "viewer"} {
		credentials := RunCredentials{}
		argv := []string{"git", "ls-remote", "--", origin + "/repo.git"}
		if speaker != "viewer" {
			credentials.GitHTTPHosts = map[string]string{origin: "other"}
			argv = []string{"sh", "-ec", readOnlyProbe, "git-tools", toolsRoot, origin + "/repo.git", driver}
		}
		command := WithCommandEnvironment(CommandSpec{Argv: argv, CredentialBindings: []CredentialBinding{BindGitWorkspace}}, map[string]string{
			SessionGatewayEnv: origin + "/relay/" + speaker, "GIT_CONFIG_GLOBAL": os.DevNull, "NO_PROXY": "*", "no_proxy": "*",
		})
		out, err := WithSandboxCredentials(sb, credentials).Exec(t.Context(), command, nil)
		if speaker == "viewer" {
			if err == nil {
				t.Fatal("a speaker without a token authenticated to the private repository")
			}
			continue
		}
		if err != nil || !strings.Contains(out, "refs/heads/main") {
			t.Fatalf("%s Git operation with read-only helpers: %v (%s)", speaker, err, out)
		}
		if driver == domain.SandboxDriverDocker {
			identity, err := ReadSandboxEnvironmentIdentity(t.Context(), sb)
			if err != nil || dockerIdentity != identity {
				t.Fatalf("first and later credential bindings must preserve the Docker container: %v", err)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, []string{"alice", "bob"}) {
		t.Fatalf("Git used credentials in the wrong turns: %v", seen)
	}
}

func TestNativeScriptWithGitCredentialOnHostWithoutGit(t *testing.T) {
	bin := t.TempDir()
	if err := os.Symlink("/bin/sh", filepath.Join(bin, "sh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	workspace := t.TempDir()
	const host = "http://git.example.invalid:8080"
	credentials := RunCredentials{GitHTTPCredentials: map[string]domain.GitHTTPExecutionCredential{
		host: {GitHTTPCredential: domain.GitHTTPCredential{Host: host, Platform: domain.GitPlatformOther}, Token: "fixture-token"},
	}}
	script := CommandSpec{Argv: []string{"sh", "-ec", "printf 'script ran'"}, CredentialBindings: []CredentialBinding{BindGit}}
	if out, err := ExecuteLocalCommand(t.Context(), workspace, script, credentials, nil); err != nil || strings.TrimSpace(out) != "script ran" {
		t.Fatalf("script with an HTTP Git token on a host without Git: %q, %v", out, err)
	}
	git := CommandSpec{Argv: []string{"git", "--version"}, CredentialBindings: []CredentialBinding{BindGit}}
	if _, err := ExecuteLocalCommand(t.Context(), workspace, git, credentials, nil); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing Git must fail only when invoked: %v", err)
	}
}

func TestDockerScriptWithGitCredentialOnImageWithoutGit(t *testing.T) {
	root := gitHelperDockerTestRoot(t)
	host := os.Getenv("AGENTFLOW_TEST_GIT_HELPERS_HOST")
	if host == "" {
		t.Fatal("AGENTFLOW_TEST_GIT_HELPERS_HOST must reach this test process")
	}
	t.Setenv("AGENTFLOW_CONVERSATION_RELAY_HOST", host)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	// Stage the packaged Git outside PATH so the LOOP can install it without
	// network access. Leave its standard Git helpers and libraries in place.
	dockerfile := domain.DockerfileDefinition{Content: "FROM " + gitHelperDockerTestImage + "\n" +
		"RUN mkdir /opt/git-fixture && mv /usr/bin/git /opt/git-fixture/git\n"}
	sandbox, err := OpenRemoteSandbox(ctx, "script-no-git-"+filepath.Base(root), filepath.Join(root, "workspace"),
		domain.SandboxProfile{Driver: domain.SandboxDriverDocker}, SandboxRequirements{}, SandboxResources{Dockerfile: dockerfile})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Close(context.Background()) })

	tokens := map[string]string{"alice": "alice-fixture-value", "bob": "bob-fixture-value"}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if !ok || tokens[user] == "" || tokens[user] != token || r.URL.Path != "/"+user+".git/info/refs" {
			w.Header().Set("WWW-Authenticate", `Basic realm="Git fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("0123456789012345678901234567890123456789\trefs/heads/main\n"))
	}))
	_ = server.Listener.Close()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + net.JoinHostPort(host, port)
	credentialsFor := func(user, endpoint string) RunCredentials {
		return RunCredentials{GitHTTPCredentials: map[string]domain.GitHTTPExecutionCredential{
			endpoint: {GitHTTPCredential: domain.GitHTTPCredential{Host: endpoint, Platform: domain.GitPlatformOther, Username: user}, Token: tokens[user]},
		}}
	}

	script := CommandSpec{Argv: []string{"sh", "-ec", "printf 'script ran'"}, CredentialBindings: []CredentialBinding{BindGit}}
	if out, err := sandbox.Exec(ctx, script, nil); err != nil || !strings.Contains(out, "script ran") {
		t.Fatalf("script before configuring a Git token: %q, %v", out, err)
	}
	before, err := ReadSandboxEnvironmentIdentity(ctx, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	withToken := WithSandboxCredentials(sandbox, credentialsFor("alice", origin))
	if out, err := withToken.Exec(ctx, script, nil); err != nil || !strings.Contains(out, "script ran") {
		t.Fatalf("script with an HTTP Git token on an image without Git: %q, %v", out, err)
	}
	git := CommandSpec{Argv: []string{"git", "--version"}, CredentialBindings: []CredentialBinding{BindGit}}
	out, err := withToken.Exec(ctx, git, nil)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 127 || !strings.Contains(out, "git: not found") {
		t.Fatalf("missing Git must fail only when invoked: %q, %v", out, err)
	}
	const installAndFetch = `
export PATH="$HOME/bin:$PATH"
if ! command -v git >/dev/null; then
  mkdir -p "$HOME/bin"
  install /opt/git-fixture/git "$HOME/bin/git"
fi
git --version
exec git ls-remote -- "$1"
`
	fetch := func(user string) CommandSpec {
		return WithCommandEnvironment(CommandSpec{
			Argv:               []string{"sh", "-ec", installAndFetch, "git-fixture", origin + "/" + user + ".git"},
			CredentialBindings: []CredentialBinding{BindGit},
		}, map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "NO_PROXY": "*", "no_proxy": "*"})
	}
	for _, user := range []string{"alice", "bob"} {
		bound := WithSandboxCredentials(sandbox, credentialsFor(user, origin))
		if out, err := bound.Exec(ctx, fetch(user), nil); err != nil || !strings.Contains(out, "refs/heads/main") {
			t.Fatalf("%s could not install Git and authenticate in the same LOOP: %q, %v", user, out, err)
		}
	}
	for _, creds := range []RunCredentials{{}, credentialsFor("alice", "http://"+net.JoinHostPort(host, "1"))} {
		bound := WithSandboxCredentials(sandbox, creds)
		if out, err := bound.Exec(ctx, fetch("alice"), nil); err == nil || !strings.Contains(out, "terminal prompts disabled") {
			t.Fatalf("Git authenticated without a matching credential: %q, %v", out, err)
		}
	}
	if after, err := ReadSandboxEnvironmentIdentity(ctx, sandbox); err != nil || after != before {
		t.Fatalf("the workspace container changed after using the token: %v", err)
	}
}

func TestDockerGitToolsRefreshAfterLegacyContainerBecomesIdle(t *testing.T) {
	root := gitHelperDockerTestRoot(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	workspace := filepath.Join(root, "workspace")
	sandbox, err := OpenRemoteSandbox(ctx, "shared-git-legacy-"+filepath.Base(root), workspace,
		domain.SandboxProfile{Driver: domain.SandboxDriverDocker}, SandboxRequirements{}, SandboxResources{Dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}})
	if err != nil {
		t.Fatal(err)
	}
	sb := sandbox.(*dockerSandbox)
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--network", "none", "--name", sb.name,
		"-v", workspace+":/workspace", "--entrypoint", "sh", gitHelperDockerTestImage, "-c", "exec sleep infinity").CombinedOutput(); err != nil {
		t.Fatalf("create legacy test container: %v (%s)", err, out)
	}
	workflow := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{
		ID: "prepare", Name: "Prepare", Type: "script", RunOnWorkspaceRebuild: true,
		Script: "printf restored > /tmp/loopforge-git-prepared",
	}}}
	old, err := EnsureWorkspacePreparation(ctx, sb, workflow, nil, nil)
	if err != nil || old.Status != "ready" {
		t.Fatalf("regular preparation must still use the existing container: %+v, %v", old, err)
	}
	check := func(script string) {
		t.Helper()
		if out, err := sb.Exec(ctx, CommandSpec{Argv: []string{"sh", "-ec", script}}, nil); err != nil {
			t.Fatalf("check Docker state: %v (%s)", err, out)
		}
	}
	check(`printf keep > shared-project; printf live > /tmp/manual-state`)
	command := WithCommandEnvironment(CommandSpec{Argv: []string{"git", "--version"}, CredentialBindings: []CredentialBinding{BindGitWorkspace}},
		map[string]string{SessionGatewayEnv: "http://127.0.0.1:1"})
	credentials := RunCredentials{GitHTTPHosts: map[string]string{"http://127.0.0.1:1": "other"}}
	if _, err := WithSandboxCredentials(sb, credentials).Exec(ctx, command, nil); !errors.Is(err, errDockerGitToolsMountMissing) {
		t.Fatalf("credential binding should report the missing mount: %v", err)
	}
	check(`test "$(cat /tmp/manual-state)" = live`)
	checkOldContainer := func() {
		t.Helper()
		if current, err := ReadSandboxEnvironmentIdentity(ctx, sb); err != nil || current != old.Environment {
			t.Fatalf("replaced the legacy Docker container while it was still in use: %v", err)
		}
	}
	checkOldContainer()

	var terminals TerminalPool
	terminal, err := terminals.HandleSandbox(ctx, sb, "workspace:1", domain.WorkspaceTerminalRequest{Operation: "open"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer terminals.CloseScope("workspace:1")
	pty := terminalPTYTest{t: t, pool: &terminals, scope: "workspace:1", id: terminal.ID}
	pty.waitFor("/workspace")
	if err := RefreshWorkspaceGitTools(ctx, sb); err != nil {
		t.Fatal(err)
	}
	checkOldContainer()
	if _, err := terminals.HandleSandbox(ctx, nil, "workspace:1", domain.WorkspaceTerminalRequest{Operation: "close", ID: terminal.ID}, nil); err != nil {
		t.Fatal(err)
	}

	// An exec initiated by the previous Worker has no active Go handle here.
	inherited := exec.CommandContext(ctx, "docker", "exec", "-i", sb.name, "sh", "-ec", "echo ready; read -r finish; test \"$finish\" = done")
	stdin, err := inherited.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := inherited.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := inherited.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if inherited.ProcessState == nil {
			_ = inherited.Process.Kill()
			_ = inherited.Wait()
		}
	}()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("start inherited Docker execution: %q, %v", line, err)
	}
	if err := RefreshWorkspaceGitTools(ctx, sb); err != nil {
		t.Fatal(err)
	}
	checkOldContainer()
	check(`test "$(cat /tmp/manual-state)" = live`)
	if _, err := io.WriteString(stdin, "done\n"); err != nil {
		t.Fatal(err)
	}
	if err := inherited.Wait(); err != nil {
		t.Fatal(err)
	}

	if err := RefreshWorkspaceGitTools(ctx, sb); err != nil {
		t.Fatal(err)
	}
	prepared, err := EnsureWorkspacePreparation(ctx, sb, workflow, nil, nil)
	if err != nil || prepared.Status != "ready" || prepared.Environment == old.Environment {
		execs, _ := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .ExecIDs}}", sb.name).Output()
		processes, _ := exec.CommandContext(ctx, "docker", "top", sb.name).Output()
		t.Fatalf("normal preparation must initialize the replacement environment: %+v, %v; execs=%s processes=%s", prepared, err, execs, processes)
	}
	check(`test "$(cat shared-project)" = keep; test "$(cat /tmp/loopforge-git-prepared)" = restored; test ! -e /tmp/manual-state`)
	if out, err := WithSandboxCredentials(sb, credentials).Exec(ctx, command, nil); err != nil || !strings.Contains(out, "git version") {
		t.Fatalf("Git cannot use the new container's mount: %v (%s)", err, out)
	}
	if err := RefreshWorkspaceGitTools(ctx, sb); err != nil {
		t.Fatal(err)
	}
	if current, err := ReadSandboxEnvironmentIdentity(ctx, sb); err != nil || current != prepared.Environment {
		t.Fatalf("preparation replaced a container that already had the read-only mount: %v", err)
	}
}

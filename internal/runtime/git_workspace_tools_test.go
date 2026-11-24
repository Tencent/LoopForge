package runtime

import (
	"context"
	"encoding/json"
	"errors"
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
			toolsRoot := nativeGitToolsRoot()
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
			} else if !NativeSandboxAvailable(driver) {
				t.Skip("native sandbox unavailable")
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
		mounts, err := inspectDockerWorkspaceMounts(t.Context(), sb.(*dockerSandbox).name)
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

func TestDockerGitBindingPreservesLegacyContainer(t *testing.T) {
	root := gitHelperDockerTestRoot(t)
	ctx := t.Context()
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
	if current, err := ReadSandboxEnvironmentIdentity(ctx, sb); err != nil || current != old.Environment {
		t.Fatalf("ordinary Git binding replaced the legacy container: %v", err)
	}
	if err := sb.Recreate(ctx); err != nil {
		t.Fatal(err)
	}
	prepared, err := EnsureWorkspacePreparation(ctx, sb, workflow, nil, nil)
	if err != nil || prepared.Status != "ready" || prepared.Environment == old.Environment {
		t.Fatalf("normal preparation must initialize the replacement environment: %+v, %v", prepared, err)
	}
	check(`test "$(cat shared-project)" = keep; test "$(cat /tmp/loopforge-git-prepared)" = restored; test ! -e /tmp/manual-state`)
	if out, err := WithSandboxCredentials(sb, credentials).Exec(ctx, command, nil); err != nil || !strings.Contains(out, "git version") {
		t.Fatalf("Git cannot use the new container's mount: %v (%s)", err, out)
	}
}

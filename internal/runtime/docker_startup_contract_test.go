package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

const fakeDockerRunningWorkspace = `{"Status":"running","Running":true,"ID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","StartedAt":"2026-01-01T00:00:00Z"}`
const fakeWorkspaceGeneration = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa|2026-01-01T00:00:00Z"

func TestDockerWorkspaceLoginProbe(t *testing.T) {
	for _, test := range []struct{ name, profile, want string }{
		{"prompt without newline", `printf 'login banner';`, ""},
		{"exit before probe", `printf 'login banner'; exit 0;`, "without executing the readiness probe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LOGIN_PROFILE", test.profile)
			useFakeCommands(t, map[string]string{"docker": `#!/bin/sh
case "$1" in
  exec) sh -c "$LOGIN_PROFILE ""${5#cd /workspace && }" "$6" "$7";;
  inspect) echo '` + fakeDockerRunningWorkspace + `' ;;
esac`})
			err := startDockerWorkspace(t.Context(), "workspace", "test-image")
			if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "login banner")) {
				t.Fatalf("unexpected probe result: %v", err)
			}
		})
	}
}

func TestDockerWorkspaceStartupErrorKeepsSafeDiagnostics(t *testing.T) {
	useFakeCommands(t, map[string]string{"docker": `#!/bin/sh
case "$1" in
  start) echo 'OCI runtime create failed: permission denied; private diagnostics' >&2; exit 1;;
  inspect) echo '{"Status":"exited","ExitCode":137,"OOMKilled":true}';;
esac`})
	err := startDockerWorkspace(t.Context(), "workspace", "test-image")
	if err == nil || strings.Contains(err.Error(), "private diagnostics") {
		t.Fatalf("unexpected public error: %v", err)
	}
	for _, want := range []string{"start workspace", "test-image", "permission denied", "exit status 1", "exit=137", "OOMKilled=true"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q from %v", want, err)
		}
	}
}

func TestDockerBrowserDoesNotReplaceOnUnknownSidecar(t *testing.T) {
	useFakeCommands(t, map[string]string{"docker": `#!/bin/sh
case "$*" in
  "inspect --type container --format "*'.State.Status'*) echo '` + fakeDockerRunningWorkspace + `' ;;
  "inspect --type container --format "*) echo 'private daemon response' >&2; exit 1;;
  "run "*|"rm "*) touch "$UNEXPECTED_MUTATION";;
esac`})
	t.Setenv("AGENTFLOW_BROWSER_IMAGE", "test-browser")
	marker := t.TempDir() + "/mutation"
	t.Setenv("UNEXPECTED_MUTATION", marker)
	sandbox := &dockerSandbox{name: "workspace", workspace: t.TempDir(), started: true}
	if _, err := sandbox.ensureBrowser(t.Context(), "run", nil); err == nil || !strings.Contains(err.Error(), "inspect browser sidecar") || strings.Contains(err.Error(), "private daemon response") {
		t.Fatalf("unexpected sidecar error: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("sidecar was modified despite unknown status: %v", err)
	}
}

func TestDockerBrowserKeepsCachedViewOnUnknownWorkspace(t *testing.T) {
	useFakeCommands(t, map[string]string{"docker": `#!/bin/sh
case "$1" in
  inspect) echo '{"Status":"running","Running":true}' ;;
  ps) exit 1;;
esac`})
	const runID = "unknown-workspace-view"
	client := &cdpClient{done: make(chan struct{})}
	browserViews.Lock()
	browserViews.items[runID] = &browserView{client: client, endpoint: browserEndpoint{Kind: "docker", DockerOwner: "workspace", DockerGeneration: fakeWorkspaceGeneration}}
	browserViews.Unlock()
	t.Cleanup(func() {
		browserViews.Lock()
		delete(browserViews.items, runID)
		browserViews.Unlock()
	})
	if _, err := openBrowserView(t.Context(), runID); err == nil || client.closed() || errors.Is(err, errDockerWorkspaceUnavailable) {
		t.Fatalf("unknown workspace must not close shared CDP: %v", err)
	}
	if BrowserNeedsStart(t.Context(), runID) {
		t.Fatal("unknown endpoint lookup must not trigger browser start")
	}
}

func TestDockerWorkspaceStartupContractWithDocker(t *testing.T) {
	base := os.Getenv("AGENTFLOW_DOCKER_START_TEST_IMAGE")
	if base == "" {
		t.Skip("set AGENTFLOW_DOCKER_START_TEST_IMAGE to the official LoopForge v0.4.2 image")
	}
	t.Setenv("AGENTFLOW_DOCKER_NETWORK", "none")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	workspace := t.TempDir()
	sandbox := &dockerSandbox{name: "agentflow-start-contract-" + rand.Text(), workspace: workspace,
		dockerfile: domain.DockerfileDefinition{Content: "FROM " + base + "\nWORKDIR /workspace\nLABEL loopforge.test=" + rand.Text() + "\n"}}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = removeDockerContainer(cleanup, sandbox.name)
		_ = exec.CommandContext(cleanup, "docker", "image", "rm", cachedDockerImageName(sandbox.dockerfile)).Run()
	})
	if err := sandbox.BindWorkspace(ctx); err != nil {
		t.Fatal(err)
	}
	if entry := docker("image", "inspect", "--format", "{{json .Config.Entrypoint}}", sandbox.builtImage); entry != `["/app/bin/agentflow"]` {
		t.Fatalf("expected the custom image to retain the inherited entrypoint, got %s", entry)
	}
	var argv [2][]string
	if err := json.Unmarshal([]byte(docker("inspect", "--format", `[{{json .Config.Entrypoint}},{{json .Config.Cmd}}]`, sandbox.name)), &argv); err != nil || !slices.Equal(argv[0], []string{"sh"}) || !slices.Equal(argv[1], []string{"-c", dockerWorkspaceKeepalive}) {
		t.Fatalf("unexpected workspace argv: %v (%v)", argv, err)
	}
	if out := docker("exec", sandbox.name, "sh", "-lc", `cd /workspace && pwd`); out != "/workspace" {
		t.Fatalf("docker exec output: %s", out)
	}
	legacyName := sandbox.name + "-legacy"
	legacyID := docker("create", "--name", legacyName, "--network", "none", "-v", workspace+":/workspace", sandbox.builtImage, "sh", "-lc", "sleep infinity")
	t.Cleanup(func() { _ = removeDockerContainer(context.Background(), legacyID) })
	legacy := &dockerSandbox{name: legacyName, workspace: workspace, dockerfile: sandbox.dockerfile}
	if err := legacy.BindWorkspace(ctx); err == nil || !strings.Contains(err.Error(), "docker rm -f "+legacyName) {
		t.Fatalf("expected explicit rebuild instruction, got %v", err)
	}
	if err := legacy.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := docker("inspect", "--format", "{{.Id}} {{.State.Status}}", legacyName); got != legacyID+" created" {
		t.Fatalf("legacy container was started or replaced: %s", got)
	}
}

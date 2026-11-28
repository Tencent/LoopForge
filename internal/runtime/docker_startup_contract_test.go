package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

const fakeWorkspaceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const fakeStartedAt = "2026-01-01T00:00:00Z"
const fakeWorkspaceGeneration = fakeWorkspaceID + "|" + fakeStartedAt
const fakeDockerRunningWorkspace = `{"Status":"running","Running":true,"Paused":false,"OOMKilled":false,"ExitCode":0,"ID":"` + fakeWorkspaceID + `","StartedAt":"` + fakeStartedAt + `"}`
const fakeDockerExited = `{"Status":"exited","Running":false,"Paused":false,"OOMKilled":true,"ExitCode":137,"ID":"` + fakeWorkspaceID + `","StartedAt":"` + fakeStartedAt + `"}`

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
  inspect) echo '` + fakeDockerExited + `';;
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

func TestDockerWorkspaceStartupParentCancellation(t *testing.T) {
	useFakeCommands(t, map[string]string{"docker": `#!/bin/sh
if [ "$1" = exec ]; then touch "$PROBE_ENTERED"; exec sleep 60; fi`})
	entered := t.TempDir() + "/probe"
	t.Setenv("PROBE_ENTERED", entered)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- startDockerWorkspace(ctx, "workspace", "test-image") }()
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(entered); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("login probe did not start")
		}
	}
	cancel()
	if err := <-done; err != context.Canceled || !IsSandboxInterrupt(err) {
		t.Fatalf("parent cancellation must remain a sandbox interrupt: %v", err)
	}
}

func TestDockerWorkspaceAndBrowserSafeFailureDiagnostics(t *testing.T) {
	for _, test := range []struct {
		stage, commands, private string
		want                     []string
	}{
		{"probe", `echo "sh: can't cd to /workspace: private-cd" >&2; exit 1`, "private-cd", []string{"workspace shell probe", "cannot enter /workspace", "exit status 1"}},
		{"probe", `echo private-probe >&2; exit 126`, "private-probe", []string{"workspace shell probe", "login shell could not be executed", "exit status 126"}},
		{"keepalive", `echo private-exec >&2; exit 1`, "private-exec", []string{"exit=127", "sleep infinity", "was not found"}},
		{"retire", `echo 'permission denied: private-retire' >&2; exit 23`, "private-retire", []string{"retire previous workspace", "permission denied", "exit status 23"}},
		{"browser", `echo private-sidecar >&2; exit 1`, "private-sidecar", []string{"browser sidecar readiness", dockerBrowserContainerName("workspace"), "exit=137", "OOMKilled=true"}},
	} {
		t.Run(test.stage+"-"+test.private, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			t.Setenv("AGENTFLOW_BROWSER", "always")
			t.Setenv("AGENTFLOW_BROWSER_IMAGE", "test-browser")
			t.Setenv("CASE_STAGE", test.stage)
			workspace := t.TempDir()
			t.Setenv("WORKSPACE", workspace)
			useFakeCommands(t, map[string]string{"docker": `#!/bin/sh
case "$1" in
  inspect)
    if [ "$6" != workspace ]; then
      [ -f "$WORKSPACE/sidecar-created" ] || { echo "Error response from daemon: No such container: $6" >&2; exit 1; }
      echo '` + strings.TrimSuffix(fakeDockerExited, "}") + `,"Image":"test-browser"}'; exit 0
    fi
    case "$5" in
      *'.State.Status'*)
        if [ "$CASE_STAGE" = keepalive ]; then
          echo '` + strings.NewReplacer(`"ExitCode":137`, `"ExitCode":127`, `"OOMKilled":true`, `"OOMKilled":false`).Replace(fakeDockerExited) + `'; exit 0
        fi
        echo '` + fakeDockerRunningWorkspace + `' ;;
      *)
        extra_mount=
        if [ "$CASE_STAGE" = retire ]; then extra_mount=',{"Destination":"/workspace/.agentflow/isolation/empty-dir"}'; fi
        printf '{"image":"test-image","mounts":[{"Destination":"/workspace","Source":"%s"}%s],"entrypoint":["sh"],"command":["-c","cd /workspace && exec sleep infinity"]}\n' "$WORKSPACE" "$extra_mount" ;;
    esac;;
  exec|rm) ` + test.commands + ` ;;
  run) touch "$WORKSPACE/sidecar-created";;
esac`})
			sandbox := &dockerSandbox{name: "workspace", workspace: workspace,
				started:    test.stage == "browser",
				dockerfile: domain.DockerfileDefinition{Content: "FROM test-image"}}
			var events []domain.TelemetryEvent
			_, err := PrepareSandboxBrowser(ctx, sandbox, workspace, "run", func(event domain.TelemetryEvent) { events = append(events, event) })
			if err == nil || strings.Contains(err.Error(), test.private) {
				t.Fatalf("missing or unsafe diagnostic: %v", err)
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in %v", want, err)
				}
			}
			if len(events) == 0 || events[len(events)-1].Name != "browser.unavailable" || !strings.Contains(events[len(events)-1].Message, err.Error()) {
				t.Errorf("no warning with the sanitized diagnostic: %+v", events)
			}
			for _, event := range events {
				raw, _ := json.Marshal(event)
				if strings.Contains(string(raw), test.private) {
					t.Errorf("visible event leaked the private diagnostic: %s", raw)
				}
			}
		})
	}
}

func TestDockerBrowserPanelPreservesSafeListDiagnostic(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	useFakeCommands(t, map[string]string{"docker": "#!/bin/sh\necho 'Cannot connect to the Docker daemon: private-socket-marker' >&2\nexit 1\n"})
	state, err := HandleBrowser(t.Context(), "unavailable-browser-daemon", domain.BrowserRequest{Operation: "status"})
	if err != nil || state.Available || strings.Contains(state.Reason, "private-socket-marker") {
		t.Fatalf("unexpected browser panel response: %+v, %v", state, err)
	}
	for _, want := range []string{"list browser sidecars", "daemon unreachable", "exit status 1"} {
		if !strings.Contains(state.Reason, want) {
			t.Errorf("missing %q in browser panel diagnostic %q", want, state.Reason)
		}
	}
}

func TestDockerWorkspaceCreatedStateIsConfirmedUnavailable(t *testing.T) {
	created := strings.NewReplacer(`"Status":"running"`, `"Status":"created"`, `"Running":true`, `"Running":false`, fakeStartedAt, "0001-01-01T00:00:00Z").Replace(fakeDockerRunningWorkspace)
	useFakeCommands(t, map[string]string{"docker": "#!/bin/sh\necho '" + created + "'"})
	if _, err := dockerWorkspaceGeneration(t.Context(), "workspace"); !errors.Is(err, errDockerWorkspaceUnavailable) || !strings.Contains(err.Error(), "state=created") {
		t.Fatalf("a never-started workspace is confirmed unavailable, not unknown: %v", err)
	}
}

func TestDockerBrowserUnknownInspection(t *testing.T) {
	for _, test := range []struct{ name, response string }{
		{"timeout", `exec sleep 60`},
		{"incomplete exited state", `if [ "$6" = workspace ]; then echo '{"Status":"exited"}'; else echo '{"Status":"exited","Running":false,"ID":"` + fakeWorkspaceID + `","Image":"test-browser"}'; fi`},
		{"invalid running generation", `echo '{"Status":"running","Running":true}'`},
		{"not-found text in ordinary error", `printf '\n'; echo 'private-inspect-marker: checking no such container for unrelated request' >&2; exit 1`},
		{"not-found for different object", `printf '\n'; echo 'Error response from daemon: No such container: unrelated' >&2; exit 1`},
		{"not-found accompanied by data", `echo '{}'; echo "Error response from daemon: No such container: $6" >&2; exit 1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			useFakeCommands(t, map[string]string{"docker": `#!/bin/sh
case "$1" in
  inspect)
    if [ "$6" = workspace ] && [ "$FAIL_INSPECT" = sidecar ]; then echo '` + fakeDockerRunningWorkspace + `'; exit 0; fi
    ` + test.response + ` ;;
  ps) printf 'browser\tworkspace\t%s\n' '` + fakeWorkspaceGeneration + `';;
  run|rm) touch "$UNEXPECTED_MUTATION";;
esac`})
			t.Setenv("AGENTFLOW_BROWSER_IMAGE", "test-browser")
			marker := t.TempDir() + "/mutation"
			t.Setenv("UNEXPECTED_MUTATION", marker)
			const runID = "unknown-workspace-view"
			client := newCDPClient(newFakeCDPTransport(), nil)
			defer client.close()
			browserViews.Lock()
			browserViews.items[runID] = &browserView{client: client, endpoint: browserEndpoint{Kind: "docker", DockerOwner: "workspace", DockerGeneration: fakeWorkspaceGeneration}}
			browserViews.Unlock()
			t.Cleanup(func() {
				browserViews.Lock()
				delete(browserViews.items, runID)
				browserViews.Unlock()
			})
			t.Setenv("FAIL_INSPECT", "workspace")
			if _, err := openBrowserView(t.Context(), runID); err == nil || client.closed() || errors.Is(err, errDockerWorkspaceUnavailable) || strings.Contains(err.Error(), "private-inspect-marker") {
				t.Errorf("unknown workspace must not close shared CDP: %v (closed=%v)", err, client.closed())
			}
			if BrowserNeedsStart(t.Context(), runID, true) {
				t.Error("successful ps with unknown owner must not trigger browser start")
			}
			t.Setenv("FAIL_INSPECT", "sidecar")
			sandbox := &dockerSandbox{name: "workspace", workspace: t.TempDir(), started: true}
			if _, err := sandbox.ensureBrowser(t.Context(), runID, nil); err == nil || strings.Contains(err.Error(), "private-inspect-marker") {
				t.Errorf("unexpected sidecar error: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("sidecar was modified despite unknown status: %v", err)
			}
		})
	}
}

func TestDockerWorkspaceLifecycleRequiresExactNotFound(t *testing.T) {
	for _, test := range []struct{ name, response string }{
		{"ordinary error", `printf '\n'; echo 'private-marker: checking no such container' >&2`},
		{"another object", `printf '\n'; echo 'Error response from daemon: No such container: unrelated' >&2`},
		{"unexpected stdout", `echo '{}'; echo "Error response from daemon: No such container: $target" >&2`},
	} {
		t.Run(test.name, func(t *testing.T) {
			useFakeCommands(t, map[string]string{"docker": `#!/bin/sh
case "$1" in
  inspect) target="$6";;
  rm) target="$3";;
  *) exit 99;;
esac
` + test.response + `
exit 1`})
			_, inspectErr := inspectDockerWorkspace(t.Context(), "workspace")
			for stage, err := range map[string]error{
				"inspect workspace": inspectErr,
				"remove container":  removeDockerContainer(t.Context(), "workspace"),
			} {
				if err == nil || !strings.Contains(err.Error(), stage) || strings.Contains(err.Error(), "private-marker") {
					t.Errorf("%s must preserve a safe failure, got %v", stage, err)
				}
			}
		})
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
	t.Run("first sidecar", func(t *testing.T) {
		image := "loopforge-test-browser:" + strings.ToLower(rand.Text())
		buildDir := t.TempDir()
		dockerfile := "FROM " + base + "\nENTRYPOINT [\"node\",\"-e\",\"require('http').createServer((req,res)=>res.end('ok')).listen(8931, '127.0.0.1')\"]\n"
		if err := os.WriteFile(filepath.Join(buildDir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
			t.Fatal(err)
		}
		name := dockerBrowserContainerName(sandbox.name)
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = removeDockerContainer(cleanup, name)
			_ = exec.CommandContext(cleanup, "docker", "image", "rm", image).Run()
		})
		docker("build", "-t", image, buildDir)
		t.Setenv("AGENTFLOW_BROWSER_IMAGE", image)
		if _, err := sandbox.ensureBrowser(ctx, sandbox.name, nil); err != nil {
			t.Fatal(err)
		}
		ownerID := docker("inspect", "--format", "{{.Id}}", sandbox.name)
		if got := docker("inspect", "--format", "{{.State.Running}} {{.HostConfig.NetworkMode}}", name); got != "true container:"+ownerID {
			t.Fatalf("expected a running sidecar in the workspace network, got %s", got)
		}
	})
}

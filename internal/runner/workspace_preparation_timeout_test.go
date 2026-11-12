package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"agentflow/internal/scriptview"
	"agentflow/internal/workspaceprep"
)

func TestRunnerWorkspacePreparationDockerPhaseTimeouts(t *testing.T) {
	for _, tt := range []struct {
		name, buildTimeout, buildDelay, prepareDelay, errorLabel string
	}{
		{name: "cold build outlasts prepare timeout", buildTimeout: "4s", buildDelay: "2", prepareDelay: "0"},
		{name: "build deadline", buildTimeout: "1s", buildDelay: "2", prepareDelay: "0", errorLabel: "正在准备沙箱环境"},
		{name: "prepare deadline", buildTimeout: "4s", buildDelay: "0", prepareDelay: "2", errorLabel: "正在初始化工作区"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, req, root := runnerPreparationFixture(t)
			req.Sandbox.Profile.Driver = "docker"
			req.Sandbox.Dockerfile = &domain.DockerfileDefinition{ID: "cold-build", Content: "FROM scratch\n"}
			state := installPreparationDocker(t)
			defer d.closeAllRunSandboxes(context.Background())
			t.Setenv("AGENTFLOW_WORKSPACE_BUILD_TIMEOUT", tt.buildTimeout)
			t.Setenv("AGENTFLOW_WORKSPACE_PREPARE_TIMEOUT", "1s")
			t.Setenv("PREPARATION_TEST_BUILD_DELAY", tt.buildDelay)
			t.Setenv("PREPARATION_TEST_SCRIPT_DELAY", tt.prepareDelay)
			var mu sync.Mutex
			var progress []domain.WorkspaceOpening
			ctx := workspaceprep.WithSink(context.Background(), func(p domain.WorkspaceOpening) {
				mu.Lock()
				defer mu.Unlock()
				progress = append(progress, p)
			})
			result := d.prepareRunnerWorkspace(ctx, root, req)
			if tt.errorLabel == "" {
				var status runtime.WorkspacePreparationStatus
				if result.Status != 200 || json.Unmarshal(result.Payload, &status) != nil || status.Status != "ready" {
					t.Fatalf("cold build should finish with its own deadline: %+v", result)
				}
				if repeated := d.prepareRunnerWorkspace(ctx, root, req); repeated.Status != 200 {
					t.Fatalf("reuse prepared environment: %+v", repeated)
				}
			} else if result.Status == 200 || !strings.Contains(result.Error, tt.errorLabel) || !strings.Contains(result.Error, "context deadline exceeded") {
				t.Fatalf("want deadline in %s, got %+v", tt.errorLabel, result)
			}
			log, err := os.ReadFile(filepath.Join(state, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			want := "build started\n"
			if tt.errorLabel != "正在准备沙箱环境" {
				want += "build finished\nprepare started\n"
			}
			if tt.errorLabel == "" {
				want += "prepare finished\n"
			}
			if string(log) != want {
				t.Fatalf("build/preparation order or reuse: got %q, want %q", log, want)
			}
			mu.Lock()
			events := append([]domain.WorkspaceOpening(nil), progress...)
			mu.Unlock()
			var sawBuildOutput bool
			for _, p := range events {
				if strings.Contains(p.Output, "cold build started") {
					sawBuildOutput = true
					if p.Phase != "build" {
						t.Errorf("build output reported in %q phase", p.Phase)
					}
				}
				if tt.errorLabel == "正在准备沙箱环境" && p.Phase == "prepare" {
					t.Error("initialization started after a failed build")
				}
			}
			if !sawBuildOutput {
				t.Error("missing live build output")
			}
			for _, entry := range d.runSandboxes {
				if entry.active != 0 {
					t.Errorf("leaked sandbox reference: %d", entry.active)
				}
			}
			if _, preparing := d.workspacePreparing.Load(requestGateKey(req)); preparing {
				t.Error("workspace still marked as preparing")
			}
			gate := d.requestGate(req)
			if !gate.TryLock() {
				t.Fatal("preparation retained the workspace fence")
			}
			gate.Unlock()
		})
	}
}

// Exercise the real lazy Docker sandbox through a controlled CLI, without a
// daemon or network. The build and initialization delays cross separate phase
// deadlines; the call log also detects duplicate builds or script replay.
func installPreparationDocker(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	helper := filepath.Join(state, "gate-mcp")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", helper)
	t.Setenv("PREPARATION_TEST_STATE", state)
	t.Setenv("PREPARATION_TEST_SCRIPT_VERSION", scriptview.Version)
	installFakeDocker(t, `#!/bin/sh
set -eu
state="$PREPARATION_TEST_STATE"
case "$1" in
version|rm|rmi|cp|start) exit 0 ;;
image)
  case "$*" in
  *Architecture*) echo amd64 ;;
  *Config.User*) echo root ;;
  *) test -f "$state/built" ;;
  esac ;;
build)
  echo 'build started' >> "$state/calls"
  echo 'cold build started'
  sleep "$PREPARATION_TEST_BUILD_DELAY"
  touch "$state/built"
  echo 'build finished' >> "$state/calls" ;;
create)
  for arg do
    case "$arg" in
    agentflow.script-view-owner=*) printf '%s' "${arg#*=}" > "$state/owner" ;;
    esac
  done
  echo container ;;
inspect)
  case "$*" in
  *'{{json .}}'*) echo '{"Id":"base","Image":"image","State":{"Running":true,"StartedAt":"epoch"}}' ;;
  *agentflow.script-view-owner*)
    if test -f "$state/owner"; then
      printf 'true|'; cat "$state/owner"
    else
      echo 'Error: No such container' >&2; exit 1
    fi ;;
  *'{{.State.Running}}|{{.Id}}|{{.State.StartedAt}}'*) echo 'true|container|epoch' ;;
  *'{{json .Mounts}}'*) echo '[]' ;;
  *) echo 'Error: No such container' >&2; exit 1 ;;
  esac ;;
exec)
  case "$*" in
  *--workspace-script-view-version*) echo "$PREPARATION_TEST_SCRIPT_VERSION" ;;
  *--workspace-script-view*)
    echo 'prepare started' >> "$state/calls"
    sleep "$PREPARATION_TEST_SCRIPT_DELAY"
    echo 'prepare finished' >> "$state/calls" ;;
  *'cd /workspace && pwd'*) echo /workspace ;;
  *) echo "unexpected docker exec: $*" >&2; exit 1 ;;
  esac ;;
*) echo "unexpected docker command: $*" >&2; exit 1 ;;
esac
`)
	return state
}

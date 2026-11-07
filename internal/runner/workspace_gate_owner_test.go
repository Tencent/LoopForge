package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A child SOP runs in its parent's checkout. Its tasks and the parent's
// preparation/restore/snapshot must share one fence.
func TestWorkspaceGateIsKeyedByWorkspaceOwner(t *testing.T) {
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}}
	child := domain.RunnerTask{ID: "child-task", RunID: "child", Workspace: "runs/parent/workspace"}
	if key := d.taskGateKey(child); key != "parent" {
		t.Fatalf("child task fenced %q, want parent", key)
	}
	legacy := domain.RunnerTask{ID: "legacy-task", RunID: "legacy", Workspace: "runs/legacy/node-a"}
	if key := d.taskGateKey(legacy); key != "legacy" {
		t.Fatalf("legacy per-node task fenced %q", key)
	}
	if key := d.taskGateKey(domain.RunnerTask{ID: "loose", RunID: "loose"}); key != "loose" {
		t.Fatalf("task outside runs/ fenced %q", key)
	}
	childRequest := domain.RunnerWorkspaceRequest{RunID: "child", WorkspaceRunID: "parent", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared}
	if d.requestGate(childRequest) != d.workspaceGate("parent") {
		t.Fatal("child request does not fence the parent workspace")
	}
	legacyRequest := domain.RunnerWorkspaceRequest{RunID: "legacy", WorkspaceRunID: "ignored"}
	if requestGateKey(legacyRequest) != "legacy" {
		t.Fatal("legacy layout must keep its own run directory")
	}
}

func TestChildSOPTaskFencesParentPreparation(t *testing.T) {
	d, request, _ := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	// The parent's environment was never stamped, so the child's task cannot be
	// joined; the parent's preparation must wait instead of running under it.
	childTask := domain.RunnerTask{ID: "child-task", RunID: "child", Workspace: "runs/prepared/workspace"}
	gate := d.workspaceGate(d.taskGateKey(childTask))
	gate.RLock()
	request.Workflow.Nodes[0].Script = "printf x >> preparation-count"
	result := d.readRunnerWorkspace(context.Background(), request)
	gate.RUnlock()
	if result.Status != 409 || result.Code != domain.WorkspaceErrorBusy {
		t.Fatalf("parent preparation ran beside child task: %+v", result)
	}
}

func TestPreparationReportsOpenExclusiveTerminal(t *testing.T) {
	d, request, _ := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	terminal := request
	terminal.Operation = domain.RunnerWorkspaceOperationTerminal
	terminal.TerminalOwner = 7
	terminal.Terminal = &domain.WorkspaceTerminalRequest{Operation: "open", ExpiresAt: time.Now().Add(time.Minute)}
	if opened := d.readRunnerWorkspace(context.Background(), terminal); opened.Status != 200 {
		t.Fatalf("open terminal: %+v", opened)
	}
	defer d.workspaceTerminals.CloseScope("prepared:7")
	result := d.readRunnerWorkspace(context.Background(), request)
	if result.Status != 409 || result.Code != domain.WorkspaceErrorTerminalActive || !strings.Contains(result.Error, "terminal") {
		t.Fatalf("paused-run terminal must be named, not reported as generic busy: %+v", result)
	}
}

// The control plane lets a paused run continue as soon as the task is
// terminal. By then the runner must have dropped its read fence.
func TestTaskReleasesWorkspaceGateBeforeReportingCompletion(t *testing.T) {
	var d *Daemon
	var heldAtComplete atomic.Int32
	heldAtComplete.Store(-1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if filepath.Base(r.URL.Path) == "complete" {
			gate := d.workspaceGate("released")
			if gate.TryLock() {
				gate.Unlock()
				heldAtComplete.Store(0)
			} else {
				heldAtComplete.Store(1)
			}
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	var err error
	d, err = New(Config{ServerURL: server.URL, BootstrapToken: "local-test", StatePath: filepath.Join(t.TempDir(), "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	d.executeTask(context.Background(), domain.RunnerTask{ID: "released-task", RunID: "released", Action: "unsupported-test-action"})
	if got := heldAtComplete.Load(); got != 0 {
		t.Fatalf("workspace gate still held when completion was reported (state %d)", got)
	}
}

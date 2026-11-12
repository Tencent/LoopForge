package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func busyPreparationRun(t *testing.T, e *Engine, st *repo.Store, id string) domain.Run {
	t.Helper()
	cvERunner(t, e, id+"-runner", domain.RunnerStatusOnline, "terminal")
	run := cvERunnerRun(id, id+"-runner")
	run.WorkflowSnapshot.Nodes = []domain.WorkflowNode{{ID: "setup", Type: "script", Script: "echo setup", RunOnWorkspaceRebuild: true}}
	run.Nodes = map[string]*domain.NodeRun{"setup": {NodeID: "setup", Status: "succeeded"}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}

// A sibling holding the runner fence (parallel branch, group role, a task
// still finishing after pause) delays a starting node; it never fails it.
func TestPrepareNodeWorkspaceWaitsForBusyRunner(t *testing.T) {
	e, st := cvEEngine(t)
	run := busyPreparationRun(t, e, st, "busy-run")
	responses := []domain.RunnerWorkspaceResponse{
		{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "workspace is in use; preparation waits for active execution or terminal to finish"},
		{Status: 409, Error: "workspace is in use; preparation waits for active execution or terminal to finish"}, // runner without code
		{Payload: json.RawMessage(`{"status":"ready","steps":[]}`)},
	}
	calls := 0
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if request.Operation != domain.RunnerWorkspaceOperationPrepare {
			t.Fatalf("unexpected operation %s", request.Operation)
		}
		response := responses[calls]
		calls++
		return response, nil
	}
	state, err := e.PrepareNodeWorkspace(context.Background(), run, nil)
	if err != nil || state.Status != "ready" || calls != 3 {
		t.Fatalf("busy runner not awaited: %+v %v calls=%d", state, err, calls)
	}
}

func TestPrepareNodeWorkspaceBusyWaitIsBoundedAndTerminalFailsFast(t *testing.T) {
	e, st := cvEEngine(t)
	run := busyPreparationRun(t, e, st, "bounded-run")
	previous := nodePreparationBusyWait
	nodePreparationBusyWait = 50 * time.Millisecond
	defer func() { nodePreparationBusyWait = previous }()
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "workspace is still in use"}, nil
	}
	if _, err := e.PrepareNodeWorkspace(context.Background(), run, nil); err == nil || !errors.Is(err, repo.ErrWorkspaceInUse) || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("endless busy wait not bounded: %v", err)
	}

	calls := 0
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		calls++
		return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorTerminalActive, Error: "workspace is in use: a workspace terminal is still open; close it before continuing"}, nil
	}
	if _, err := e.PrepareNodeWorkspace(context.Background(), run, nil); !errors.Is(err, errWorkspaceTerminalActive) || errors.Is(err, repo.ErrWorkspaceInUse) || calls != 1 {
		t.Fatalf("open terminal must fail fast with its own reason: %v calls=%d", err, calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	nodePreparationBusyWait = time.Minute
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		cancel()
		return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "workspace is still in use"}, nil
	}
	if _, err := e.PrepareNodeWorkspace(ctx, run, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pause/cancel must stop the wait: %v", err)
	}
}

func TestRunnerWorkspaceBusyClassification(t *testing.T) {
	cases := []struct {
		response domain.RunnerWorkspaceResponse
		busy     bool
	}{
		{domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "anything"}, true},
		{domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace is still in use"}, true},
		{domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace preparation is in progress"}, true},
		{domain.RunnerWorkspaceResponse{Status: 409, Error: "workspace is in use; finish active execution before preparing the terminal"}, true},
		{domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorTerminalActive, Error: "workspace is in use: a workspace terminal is still open"}, false},
		{domain.RunnerWorkspaceResponse{Status: 409, Error: "prepare workspace repositories: exit status 128"}, false},
		{domain.RunnerWorkspaceResponse{Status: 500, Error: "workspace is still in use"}, false},
	}
	for _, c := range cases {
		if got := runnerWorkspaceBusy(c.response); got != c.busy {
			t.Errorf("%+v busy=%v, want %v", c.response, got, c.busy)
		}
	}
}

// An IDE terminal holds the runner fence after the SOP finished. Refilling
// archived files must not fail the save or terminal input that triggered it.
func TestArchivedRefillWaitsWhileRunnerIsBusy(t *testing.T) {
	e, st := cvEEngine(t)
	run := busyPreparationRun(t, e, st, "refill-busy-run")
	files := []domain.WorkspaceArtifactFile{{Path: "hello.md", Content: "hi", SHA256: runtime.WorkspaceContentSHA("hi")}}
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if request.Operation != domain.RunnerWorkspaceOperationArtifacts {
			t.Fatalf("unexpected operation %s", request.Operation)
		}
		return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "workspace is still in use"}, nil
	}
	if restored, err := e.restoreWorkspaceFiles(context.Background(), run, files); err != nil || len(restored) != 0 {
		t.Fatalf("busy refill must be skipped: %v %v", restored, err)
	}
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Status: 409, Error: "restore failed: disk full"}, nil
	}
	if _, err := e.restoreWorkspaceFiles(context.Background(), run, files); err == nil {
		t.Fatal("a real refill failure must still surface")
	}
}

// The readiness shortcut reads node states. A run without them (a bare locked
// row) would look like it has nothing to prepare.
func TestPreparationPlanNeedsNodeStates(t *testing.T) {
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "setup", Type: "script", Script: "make deps", RunOnWorkspaceRebuild: true}}}}
	run.Nodes = map[string]*domain.NodeRun{"setup": {NodeID: "setup", Status: "succeeded"}}
	if runtime.WorkspacePreparationTrivial(workspacePreparationPlan(run)) {
		t.Fatal("a succeeded preparation step must keep the readiness check")
	}
	run.Nodes = nil
	if !runtime.WorkspacePreparationTrivial(workspacePreparationPlan(run)) {
		t.Fatal("expected a run without node states to look trivial; callers must pass RunState")
	}
}

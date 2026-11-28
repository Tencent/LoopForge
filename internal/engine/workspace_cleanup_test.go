package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSucceededRunnerWorkspaceQueuesImmediateDurableCleanup(t *testing.T) {
	t.Parallel()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(t.TempDir()))
	runner := domain.Runner{ID: "runner-cleanup", Name: "cleanup runner", Status: domain.RunnerStatusOffline}
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "remote-cleanup", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}

	engine.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, _ domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		payload, _ := json.Marshal(map[string]any{"checkpoint": runtime.EmptyWorkspaceCheckpoint()})
		return domain.RunnerWorkspaceResponse{Payload: payload}, nil
	}
	engine.reconcileTerminalWorkspaceCleanup()

	cleaned, _ := store.Run(run.ID)
	if cleaned.WorkspaceCleanedAt.IsZero() {
		t.Fatal("succeeded runner workspace was not marked unavailable")
	}
	task, ok := store.RunnerTask("rt-cleanup-" + run.ID)
	if !ok || task.Action != domain.RunnerTaskActionCleanupRun || task.RunnerID != runner.ID {
		t.Fatalf("durable runner cleanup was not queued: %+v", task)
	}
}

func TestRetryCancelsQueuedRunnerCleanupAndAllowsLaterCleanup(t *testing.T) {
	t.Parallel()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	e := New(store, runtime.NewManager(t.TempDir()))
	e.executeRuns = false
	runner := domain.Runner{ID: "runner-replay", Name: "replay runner", Status: domain.RunnerStatusOnline, Runtimes: []domain.RunnerRuntime{{Provider: "terminal", Status: domain.RuntimeStatusOnline}}}
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "replay-remote", Nodes: []domain.WorkflowNode{{ID: "work", Type: "command"}}}
	run := domain.Run{ID: "run-replay-remote", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "cancelled", WorkspaceCleanedAt: time.Now().UTC(), ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID}, Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "cancelled"}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	cleanupID := "rt-cleanup-" + run.ID
	if err := store.PutRunnerTask(domain.RunnerTask{ID: cleanupID, RunnerID: runner.ID, Provider: "terminal", Action: domain.RunnerTaskActionCleanupRun, RunID: run.ID, Status: domain.RunnerTaskQueued}); err != nil {
		t.Fatal(err)
	}
	// A Main Bot attempt on a legacy runner can leave a row without an archive.
	token, err := e.BeginConversationWorkspaceUse(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EndConversationWorkspaceUse(context.Background(), run.ID, token); err != nil {
		t.Fatal(err)
	}
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		t.Fatal("legacy retry attempted a snapshot RPC without a saved archive")
		return domain.RunnerWorkspaceResponse{}, nil
	}
	if err := e.Retry(run.ID, "work", "重跑", "补充上下文"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Run(run.ID)
	task, _ := store.RunnerTask(cleanupID)
	if got.Status != "running" || !got.WorkspaceCleanedAt.IsZero() || task.Status != domain.RunnerTaskCanceled {
		t.Fatalf("replayed run = %+v, cleanup = %+v", got, task)
	}
	// A previous completed cleanup must not suppress cleanup after this replay.
	task.Status = domain.RunnerTaskSuccess
	if err := store.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if err := e.queueRunnerRunCleanup(got); err != nil {
		t.Fatal(err)
	}
	task, _ = store.RunnerTask(cleanupID)
	if task.Status != domain.RunnerTaskQueued {
		t.Fatalf("later cleanup = %+v", task)
	}
}

func TestFailedWorkspaceIsNeverCleanedAutomatically(t *testing.T) {
	t.Parallel()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	engine := New(store, runtime.NewManager(t.TempDir()))
	run := domain.Run{ID: "failed-cleanup", Status: "failed", Nodes: map[string]*domain.NodeRun{}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}

	engine.reconcileTerminalWorkspaceCleanup()

	kept, _ := store.Run(run.ID)
	if !kept.WorkspaceCleanedAt.IsZero() {
		t.Fatal("failed workspace was cleaned automatically")
	}
}

func TestCompletedConversationSavesBeforeReclamationEvenWhenNotArchived(t *testing.T) {
	t.Parallel()
	e, s := newRecoveryEngine(t)
	r := domain.Run{ID: "conversation-workspace", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	session, err := s.EnsureRunAssistantSession(r.ID, "platform", "Follow-up edits", 1)
	if err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	got, _ := s.RunState(r.ID)
	if got.WorkspaceCleanedAt.IsZero() {
		t.Fatal("completed LOOP workspace was not reclaimed")
	}
	kept := false
	for _, id := range e.liveSandboxRunIDs() {
		if id == r.ID {
			kept = true
		}
	}
	if kept {
		t.Fatal("reclaimed workspace was retained by background sweep")
	}
	if _, err = s.WorkspaceCheckpointArchive(r.ID); err != nil {
		t.Fatal("reclaimed without durable snapshot", err)
	}
	if err = s.SetAssistantSessionArchived(session.UserID, session.ID, true); err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	got, _ = s.RunState(r.ID)
	if got.WorkspaceCleanedAt.IsZero() {
		t.Fatal("archived workspace was never reclaimed")
	}
}

// Regression for #1473: the runner workspace of a terminal run can disappear
// before the sweep saves its last checkpoint. That is an idempotent reclaim,
// not a permanent failure: finish cleanup, release the admission slot and let
// the sweep settle. Real RPC failures must still abort cleanup.
func TestTerminalRunnerCleanupTreatsMissingWorkspaceAsReclaimed(t *testing.T) {
	t.Parallel()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	e := New(store, runtime.NewManager(t.TempDir()))
	runner := domain.Runner{ID: "missing-ws-runner", Name: "runner", Status: domain.RunnerStatusOffline}
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "missing-ws-run", Status: "succeeded", AdmissionState: "held",
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	missing := domain.RunnerWorkspaceResponse{Status: 404, Code: domain.WorkspaceErrorMissing, Error: "workspace directory missing"}
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return missing, nil
	}
	e.reconcileTerminalWorkspaceCleanup()

	got, _ := store.RunState(run.ID)
	if got.WorkspaceCleanedAt.IsZero() {
		t.Fatal("missing runner workspace was not treated as reclaimed")
	}
	if got.AdmissionState != "released" {
		t.Fatalf("admission slot stayed held: %q", got.AdmissionState)
	}
	if task, ok := store.RunnerTask("rt-cleanup-" + run.ID); !ok || task.Action != domain.RunnerTaskActionCleanupRun {
		t.Fatalf("runner cleanup was not queued: %+v", task)
	}
}

func TestTerminalRunnerCleanupStillReportsRealTransportFailure(t *testing.T) {
	t.Parallel()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	e := New(store, runtime.NewManager(t.TempDir()))
	runner := domain.Runner{ID: "failing-ws-runner", Name: "runner", Status: domain.RunnerStatusOffline}
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "failing-ws-run", Status: "cancelled", AdmissionState: "held",
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, _ domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{}, errors.New("runner offline")
	}
	e.reconcileTerminalWorkspaceCleanup()

	got, _ := store.RunState(run.ID)
	if !got.WorkspaceCleanedAt.IsZero() || got.AdmissionState != "held" {
		t.Fatalf("real transport failure was swallowed: cleaned=%v admission=%q", got.WorkspaceCleanedAt, got.AdmissionState)
	}
}

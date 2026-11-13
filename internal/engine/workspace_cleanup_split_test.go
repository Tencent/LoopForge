package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func newCleanupSplitEngine(t *testing.T) (*Engine, *repo.Store) {
	t.Helper()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return New(store, runtime.NewManager(t.TempDir())), store
}

func putCleanedRun(t *testing.T, store *repo.Store, run domain.Run) {
	t.Helper()
	run.WorkspaceCleanedAt = time.Now().UTC()
	if run.Nodes == nil {
		run.Nodes = map[string]*domain.NodeRun{}
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
}

func workspaceStillCleaned(t *testing.T, store *repo.Store, runID string) bool {
	t.Helper()
	run, ok := store.Run(runID)
	if !ok {
		t.Fatalf("run %s missing", runID)
	}
	return !run.WorkspaceCleanedAt.IsZero()
}

func TestRearmCleanedRunnerWorkspace(t *testing.T) {
	t.Parallel()
	const runnerID = "runner-rearm"
	cases := []struct {
		name        string
		runnerID    string
		putRunner   bool
		taskStatus  string
		wantCleaned bool
	}{
		{name: "no runner keeps cleaned", runnerID: "", wantCleaned: true},
		{name: "missing task with live runner rearms", runnerID: runnerID, putRunner: true, wantCleaned: false},
		{name: "missing task with removed runner keeps cleaned", runnerID: runnerID, wantCleaned: true},
		{name: "failed task rearms", runnerID: runnerID, putRunner: true, taskStatus: domain.RunnerTaskFailed, wantCleaned: false},
		{name: "canceled task rearms", runnerID: runnerID, putRunner: true, taskStatus: domain.RunnerTaskCanceled, wantCleaned: false},
		{name: "queued task keeps cleaned", runnerID: runnerID, putRunner: true, taskStatus: domain.RunnerTaskQueued, wantCleaned: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, store := newCleanupSplitEngine(t)
			if tc.putRunner {
				if err := store.PutRunner(domain.Runner{ID: runnerID, Name: "rearm runner", Status: domain.RunnerStatusOffline}); err != nil {
					t.Fatal(err)
				}
			}
			run := domain.Run{ID: "run-rearm", Status: "succeeded"}
			putCleanedRun(t, store, run)
			if tc.taskStatus != "" {
				task := domain.RunnerTask{ID: "rt-cleanup-" + safeRunnerPath(run.ID), RunnerID: runnerID, Provider: "terminal", Action: domain.RunnerTaskActionCleanupRun, RunID: run.ID, Status: tc.taskStatus}
				if err := store.PutRunnerTask(task); err != nil {
					t.Fatal(err)
				}
			}
			e.rearmCleanedRunnerWorkspace(run, tc.runnerID)
			if got := workspaceStillCleaned(t, store, run.ID); got != tc.wantCleaned {
				t.Fatalf("cleaned = %v, want %v", got, tc.wantCleaned)
			}
		})
	}
}

func TestReclaimForeignWorkspace(t *testing.T) {
	t.Parallel()
	foreignRun := func(id string) domain.Run {
		return domain.Run{
			ID: id, Status: "succeeded", WorkspaceHost: "other-host", WorkspaceRoot: "/srv/other",
			ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner-foreign"},
		}
	}
	t.Run("unclaimed checkpoint resets without reclaim", func(t *testing.T) {
		t.Parallel()
		e, store := newCleanupSplitEngine(t)
		run := foreignRun("run-foreign-unclaimed")
		putCleanedRun(t, store, run)
		e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
			t.Fatal("reclaim requested without a claimed checkpoint")
			return domain.RunnerWorkspaceResponse{}, nil
		}
		e.reclaimForeignWorkspace(run, false)
		if workspaceStillCleaned(t, store, run.ID) {
			t.Fatal("cleanup claim was not reset")
		}
	})
	for _, tc := range []struct {
		name        string
		response    domain.RunnerWorkspaceResponse
		err         error
		wantCleaned bool
	}{
		{name: "transport error resets", err: errors.New("unreachable"), wantCleaned: false},
		{name: "response error resets", response: domain.RunnerWorkspaceResponse{Error: "busy"}, wantCleaned: false},
		{name: "success keeps cleaned", wantCleaned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, store := newCleanupSplitEngine(t)
			run := foreignRun("run-foreign-claimed")
			putCleanedRun(t, store, run)
			var got domain.RunnerWorkspaceRequest
			e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				got = request
				return tc.response, tc.err
			}
			e.reclaimForeignWorkspace(run, true)
			if got.RunID != run.ID || got.Operation != domain.RunnerWorkspaceOperationReclaim {
				t.Fatalf("reclaim request = %+v", got)
			}
			if cleaned := workspaceStillCleaned(t, store, run.ID); cleaned != tc.wantCleaned {
				t.Fatalf("cleaned = %v, want %v", cleaned, tc.wantCleaned)
			}
		})
	}
}

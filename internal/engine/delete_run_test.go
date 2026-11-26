package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

type deleteTrackingSandbox struct {
	workspace string
	closed    bool
}

func (s *deleteTrackingSandbox) ID() string        { return "delete-tracking" }
func (s *deleteTrackingSandbox) Workspace() string { return s.workspace }
func (s *deleteTrackingSandbox) Exec(context.Context, runtime.CommandSpec, runtime.EventSink) (string, error) {
	return "", nil
}
func (s *deleteTrackingSandbox) Close(context.Context) error {
	s.closed = true
	return nil
}

func TestDeleteFailedRunDestroysLocalWorkspace(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	root := t.TempDir()
	e := New(st, runtime.NewManager(root))
	run := domain.Run{ID: "failed-local", Status: "failed"}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, run.ID)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "partial.txt"), []byte("failed output"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox := &deleteTrackingSandbox{workspace: workspace}
	e.environments.envs[run.ID] = &runEnvironment{sandbox: sandbox}

	if err := e.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
	if !sandbox.closed {
		t.Fatal("run sandbox was not closed")
	}
	if _, ok := e.environments.envs[run.ID]; ok {
		t.Fatal("run environment remains registered after delete")
	}
	if _, ok := st.Run(run.ID); ok {
		t.Fatal("run record was not deleted")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after delete: %v", err)
	}
}

func TestDeleteRunRetainsRecordWhileAnotherProcessOwnsSandbox(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	root := t.TempDir()
	manager := runtime.NewManager(root)
	e := New(st, manager)
	run := domain.Run{ID: "busy-delete", Status: "failed"}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	sandbox, err := manager.AcquireRun(context.Background(), run.ID, domain.SandboxProfile{Driver: "local"}, runtime.SandboxRequirements{}, runtime.RunCredentials{}, runtime.SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteRun(run.ID); !errors.Is(err, runtime.ErrSandboxInUse) {
		t.Fatalf("delete while sandbox busy: %v", err)
	}
	if _, ok := st.Run(run.ID); !ok {
		t.Fatal("deleted run while workspace was still in use")
	}
	if err := sandbox.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRunDefersForeignOrAPIEnvironmentCleanup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		apiOnly bool
		foreign bool
	}{
		{name: "api", apiOnly: true},
		{name: "foreign worker", foreign: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			e := New(st, runtime.NewManager(t.TempDir()))
			e.executeRuns = !tc.apiOnly
			backend := &recordingExecutionBackend{}
			e.executionBackend = backend
			run := domain.Run{ID: "delete-deferred", Status: "failed"}
			if tc.foreign {
				run.WorkspaceHost, run.WorkspaceRoot = "other-worker", "/other-root"
			}
			if err := st.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if err := e.DeleteRun(run.ID); err != nil {
				t.Fatal(err)
			}
			if len(backend.destroyed) != 0 {
				t.Fatalf("destroyed on wrong host: %v", backend.destroyed)
			}
			if _, ok := st.Run(run.ID); ok {
				t.Fatal("run was not deleted")
			}
		})
	}
}

func TestWorkerReclaimsWorkspaceAfterAPIDeletesRun(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	root := t.TempDir()
	api := New(st, runtime.NewManager(root))
	api.ConfigureExecution("api", "host", 0, false)
	worker := New(st, runtime.NewManager(root))
	worker.ConfigureExecution("worker", "host", 0, true)
	run := domain.Run{ID: "deleted-on-api", Status: "failed"}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := worker.RunWorkspace(run.ID)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := api.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("API removed workspace before worker sweep: %v", err)
	}
	worker.reclaimSandboxes()
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("worker did not reclaim deleted run workspace: %v", err)
	}
}

func TestDeleteRunRefusesTerminalRunWithUnfinishedNode(t *testing.T) {
	t.Parallel()
	for _, nodeStatus := range []string{"running", "pending_input"} {
		t.Run(nodeStatus, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			e := New(st, runtime.NewManager(t.TempDir()))
			run := domain.Run{ID: "failed-with-" + nodeStatus, Status: "failed", Nodes: map[string]*domain.NodeRun{
				"done": {Status: "succeeded"},
				"busy": {Status: nodeStatus},
			}}
			if err := st.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if err := e.DeleteRun(run.ID); err == nil || !strings.Contains(err.Error(), "仍有未结束的节点") {
				t.Fatalf("DeleteRun error = %v, want unfinished node refusal", err)
			}
			if _, ok := st.Run(run.ID); !ok {
				t.Fatal("run with an unfinished node was deleted")
			}
		})
	}
}

func TestDeleteRunPreservesTerminalTaskOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		runStatus  string
		taskStatus string
	}{
		{runStatus: "succeeded", taskStatus: "completed"},
		{runStatus: "failed", taskStatus: "failed"},
		{runStatus: "cancelled", taskStatus: "cancelled"},
	} {
		t.Run(tc.runStatus, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			run := domain.Run{ID: "delete-" + tc.runStatus, Status: tc.runStatus, ExecutionContext: domain.TelemetryContext{TaskID: "task-" + tc.runStatus}}
			if err := st.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if err := st.PutTask(domain.Task{ID: run.ExecutionContext.TaskID, IssueID: "issue", RunID: run.ID, Status: "running"}); err != nil {
				t.Fatal(err)
			}
			e := New(st, runtime.NewManager(t.TempDir()))
			if err := e.DeleteRun(run.ID); err != nil {
				t.Fatal(err)
			}
			task, ok := st.Task(run.ExecutionContext.TaskID)
			if !ok || task.Status != tc.taskStatus {
				t.Fatalf("task outcome = %+v, found=%v; want %q", task, ok, tc.taskStatus)
			}
		})
	}
}

func TestFailedTransitionsKeepRunEnvironmentUntouched(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  domain.Run
		fail func(*Engine) error
	}{
		{
			name: "orphan failure",
			run:  domain.Run{ID: "failed-orphan", Status: "running", Nodes: map[string]*domain.NodeRun{"work": {Status: "running"}}},
			fail: func(e *Engine) error {
				e.settleOrphan("failed-orphan", "执行租约丢失")
				return nil
			},
		},
		{
			name: "declined retry",
			run:  domain.Run{ID: "failed-declined", Status: "waiting", Nodes: map[string]*domain.NodeRun{"work": {Status: "waiting_approval"}}},
			fail: func(e *Engine) error { return e.DeclineRetry("failed-declined", "work") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			e := New(st, runtime.NewManager(t.TempDir()))
			if err := st.PutRun(tc.run); err != nil {
				t.Fatal(err)
			}
			sandbox := &deleteTrackingSandbox{workspace: t.TempDir()}
			e.environments.envs[tc.run.ID] = &runEnvironment{sandbox: sandbox}

			if err := tc.fail(e); err != nil {
				t.Fatal(err)
			}
			failed, _ := st.Run(tc.run.ID)
			if failed.Status != "failed" {
				t.Fatalf("run status = %q", failed.Status)
			}
			if sandbox.closed {
				t.Fatal("failed transition closed the run sandbox")
			}
			if _, ok := e.environments.envs[tc.run.ID]; !ok {
				t.Fatal("failed transition released the run environment")
			}
			if !failed.WorkspaceCleanedAt.IsZero() {
				t.Fatalf("failed transition scheduled workspace cleanup: %+v", failed)
			}
		})
	}
}

func TestDeleteFailedRunnerRunQueuesRemoteCleanup(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := New(st, runtime.NewManager(t.TempDir()))
	runner := domain.Runner{ID: "runner-delete", Name: "delete runner", Status: domain.RunnerStatusOffline}
	if err := st.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID:     "failed-runner",
		Status: "failed",
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode:     domain.ExecutionModeRunner,
			ExecutionRunnerID: runner.ID,
		},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}

	if err := e.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Run(run.ID); ok {
		t.Fatal("run record was not deleted")
	}
	task, ok := st.RunnerTask("rt-cleanup-" + run.ID)
	if !ok {
		t.Fatal("runner cleanup task was not queued")
	}
	if task.Status != domain.RunnerTaskQueued || task.RunnerID != runner.ID || task.Action != domain.RunnerTaskActionCleanupRun {
		t.Fatalf("unexpected cleanup task: %+v", task)
	}
}

func TestDeleteRunAfterExecutionRunnerWasUninstalled(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := New(st, runtime.NewManager(t.TempDir()))
	run := domain.Run{
		ID:     "missing-runner",
		Status: "failed",
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode:     domain.ExecutionModeRunner,
			ExecutionRunnerID: "removed-runner",
		},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}

	if err := e.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Run(run.ID); ok {
		t.Fatal("run record was not deleted after the runner uninstall lifecycle completed")
	}
	if _, ok := st.RunnerTask("rt-cleanup-" + run.ID); ok {
		t.Fatal("cleanup task was queued for a runner that no longer exists")
	}
}

package engine

import (
	"context"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// issue #1317: after a lease takeover, the old execution's Docker sandbox
// container may still hold the Agent CLI process, which continues writing
// to the shared workspace while the new execution starts on the same
// checkout. The takeover must discard the old sandbox container so the
// stale CLI can no longer write to the workspace.

// TestLeaseTakeoverDiscardsOldSandbox verifies that resuming an orphaned
// run discards the old sandbox container before starting the new execution.
func TestLeaseTakeoverDiscardsOldSandbox(t *testing.T) {
	t.Parallel()
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("worker-takeover", "worker-takeover-host", time.Minute, true)

	now := time.Now().UTC()
	runner := domain.Runner{ID: "runner-takeover", Name: "runner-takeover", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation}
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}

	run := domain.Run{
		ID:              "run-takeover",
		WorkflowID:      "wf-takeover",
		Status:          "running",
		OwnerInstanceID: "dead-instance",
		LeaseUntil:      now.Add(-time.Minute), // expired lease
		Generation:      1,
		CreatedAt:       now,
		UpdatedAt:       now,
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID: "wf-takeover",
			Nodes: []domain.WorkflowNode{
				{ID: "work", Type: "agent"},
			},
		},
		Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running"}},
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode:         domain.ExecutionModeRunner,
			ExecutionRunnerID:     runner.ID,
			RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}

	// A live runner task that the stale execution holds. Invocation tasks
	// carry no Action; the daemon routes them by task.Invocation.
	taskID := "rt-work-run-takeover"
	if err := s.PutRunnerTask(domain.RunnerTask{
		ID: taskID, RunnerID: runner.ID, Provider: "terminal",
		Action: "", RunID: run.ID, NodeID: "work",
		Status: domain.RunnerTaskLeased, LeaseOwner: "runner-takeover",
		DesiredState: "", Attempt: 1, MaxAttempts: 3,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	resumed := e.ResumeInterruptedRuns()
	if len(resumed) != 1 || resumed[0] != run.ID {
		t.Fatalf("run was not resumed on takeover: %v", resumed)
	}

	got, _ := s.Run(run.ID)
	if got.Status != "running" || got.OwnerInstanceID != e.instanceID {
		t.Fatalf("run not taken over: status=%s owner=%s", got.Status, got.OwnerInstanceID)
	}
	task, ok := s.RunnerTask(taskID)
	if !ok {
		t.Fatal("stale runner task was removed")
	}
	if task.DesiredState != domain.RunnerTaskDesiredCancel {
		t.Fatalf("stale runner task not canceled: desired=%q", task.DesiredState)
	}

	// The old sandbox container must be discarded. cancelRunnerTasksForRun
	// calls DiscardRunContainer, which removes the Docker container so the
	// stale CLI loses its mount and can no longer write to the workspace.
	// Verify the container was discarded by checking that no idle Docker
	// sandbox remains for this run.
	if remaining, err := e.sandboxes.RunsWithIdleDockerSandbox(context.Background(), []string{run.ID}); err == nil && len(remaining) > 0 {
		t.Fatalf("old sandbox container not discarded: %v", remaining)
	}
}

// TestLeaseTakeoverCancelsRunnerTasksBeforeResume verifies that runner
// tasks are canceled before the new execution starts.
func TestLeaseTakeoverCancelsRunnerTasksBeforeResume(t *testing.T) {
	t.Parallel()
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("worker-cancel", "worker-cancel-host", time.Minute, true)

	now := time.Now().UTC()
	runner := domain.Runner{ID: "runner-cancel", Name: "runner-cancel", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation}
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}

	run := domain.Run{
		ID:               "run-cancel-tasks",
		WorkflowID:       "wf-cancel-tasks",
		Status:           "running",
		OwnerInstanceID:  "dead-instance",
		LeaseUntil:       now.Add(-time.Minute),
		Generation:       1,
		CreatedAt:        now,
		UpdatedAt:        now,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-cancel-tasks", Nodes: []domain.WorkflowNode{{ID: "work", Type: "agent"}}},
		Nodes:            map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running"}},
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode:         domain.ExecutionModeRunner,
			ExecutionRunnerID:     runner.ID,
			RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}

	for i, id := range []string{"rt-work-run-cancel-tasks", "rt-cleanup-run-cancel-tasks"} {
		action := ""
		if i == 1 {
			action = domain.RunnerTaskActionCleanupRun
		}
		if err := s.PutRunnerTask(domain.RunnerTask{
			ID: id, RunnerID: runner.ID, Provider: "terminal",
			Action: action, RunID: run.ID, NodeID: "work",
			Status: domain.RunnerTaskLeased, LeaseOwner: "runner-cancel",
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	resumed := e.ResumeInterruptedRuns()
	if len(resumed) != 1 || resumed[0] != run.ID {
		t.Fatalf("run was not resumed: %v", resumed)
	}

	for _, id := range []string{"rt-work-run-cancel-tasks", "rt-cleanup-run-cancel-tasks"} {
		task, ok := s.RunnerTask(id)
		if !ok {
			t.Fatalf("task %s removed", id)
		}
		if task.DesiredState != domain.RunnerTaskDesiredCancel {
			t.Fatalf("task %s not canceled: desired=%q", id, task.DesiredState)
		}
	}
}

// TestCancelDiscardsOldSandbox verifies that canceling a running run
// discards the old sandbox container so the stale CLI stops writing.
func TestCancelDiscardsOldSandbox(t *testing.T) {
	t.Parallel()
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("worker-cancel-sandbox", "worker-cancel-sandbox-host", time.Minute, true)

	now := time.Now().UTC()
	runner := domain.Runner{ID: "runner-cancel-sandbox", Name: "runner-cancel-sandbox", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation}
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}

	run := domain.Run{
		ID:               "run-cancel-sandbox",
		WorkflowID:       "wf-cancel-sandbox",
		Status:           "running",
		OwnerInstanceID:  e.instanceID,
		LeaseUntil:       now.Add(time.Minute),
		Generation:       1,
		CreatedAt:        now,
		UpdatedAt:        now,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-cancel-sandbox", Nodes: []domain.WorkflowNode{{ID: "work", Type: "agent"}}},
		Nodes:            map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running"}},
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode:         domain.ExecutionModeRunner,
			ExecutionRunnerID:     runner.ID,
			RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}

	if err := s.PutRunnerTask(domain.RunnerTask{
		ID: "rt-work-run-cancel-sandbox", RunnerID: runner.ID, Provider: "terminal",
		Action: "", RunID: run.ID, NodeID: "work",
		Status: domain.RunnerTaskLeased, LeaseOwner: "runner-cancel-sandbox",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Register a run environment so cancel has a sandbox to discard.
	e.runEnvMu.Lock()
	e.runEnvs[run.ID] = &runEnvironment{sandbox: stubWorkspaceSandbox{workspace: t.TempDir()}}
	e.runEnvMu.Unlock()

	if err := e.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Run(run.ID)
	if got.Status != "cancelled" {
		t.Fatalf("run not cancelled: status=%s", got.Status)
	}
	// The old sandbox container must be discarded on cancel.
	if remaining, err := e.sandboxes.RunsWithIdleDockerSandbox(context.Background(), []string{run.ID}); err == nil && len(remaining) > 0 {
		t.Fatalf("old sandbox container not discarded on cancel: %v", remaining)
	}
}

// TestPauseDiscardsOldSandbox verifies that pausing a running run
// discards the old sandbox container.
func TestPauseDiscardsOldSandbox(t *testing.T) {
	t.Parallel()
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("worker-pause-sandbox", "worker-pause-sandbox-host", time.Minute, true)

	now := time.Now().UTC()
	runner := domain.Runner{ID: "runner-pause-sandbox", Name: "runner-pause-sandbox", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation}
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}

	run := domain.Run{
		ID:               "run-pause-sandbox",
		WorkflowID:       "wf-pause-sandbox",
		Status:           "running",
		OwnerInstanceID:  e.instanceID,
		LeaseUntil:       now.Add(time.Minute),
		Generation:       1,
		CreatedAt:        now,
		UpdatedAt:        now,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-pause-sandbox", Nodes: []domain.WorkflowNode{{ID: "work", Type: "agent"}}},
		Nodes:            map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running"}},
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode:         domain.ExecutionModeRunner,
			ExecutionRunnerID:     runner.ID,
			RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}

	if err := s.PutRunnerTask(domain.RunnerTask{
		ID: "rt-work-run-pause-sandbox", RunnerID: runner.ID, Provider: "terminal",
		Action: "", RunID: run.ID, NodeID: "work",
		Status: domain.RunnerTaskLeased, LeaseOwner: "runner-pause-sandbox",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	e.runEnvMu.Lock()
	e.runEnvs[run.ID] = &runEnvironment{sandbox: stubWorkspaceSandbox{workspace: t.TempDir()}}
	e.runEnvMu.Unlock()

	if err := e.Pause(run.ID); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Run(run.ID)
	if got.Status != "waiting" || !got.PauseRequested {
		t.Fatalf("run not paused: status=%s pause=%v", got.Status, got.PauseRequested)
	}
	// The old sandbox container must be discarded on pause.
	if remaining, err := e.sandboxes.RunsWithIdleDockerSandbox(context.Background(), []string{run.ID}); err == nil && len(remaining) > 0 {
		t.Fatalf("old sandbox container not discarded on pause: %v", remaining)
	}
}

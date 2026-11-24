package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestInterruptedRunnerCleanupPreservesNewerClaim(t *testing.T) {
	t.Parallel()
	e, store := newRecoveryEngine(t)
	now := time.Now().UTC()
	runner := domain.Runner{ID: "runner-interrupted-cleanup", Status: domain.RunnerStatusOffline}
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "runner-cleanup-claim", Status: "succeeded", WorkspaceCleanedAt: now.Add(-time.Hour),
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRunnerTask(domain.RunnerTask{ID: "rt-cleanup-" + run.ID, RunnerID: runner.ID, Provider: "terminal",
		Action: domain.RunnerTaskActionCleanupRun, Status: domain.RunnerTaskFailed, RunID: run.ID}); err != nil {
		t.Fatal(err)
	}
	stale, _ := store.RunState(run.ID)
	if err := store.DB.Table("runs").Where("id = ?", run.ID).Updates(map[string]any{
		"revision": stale.Revision + 1, "workspace_cleaned_at": now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	e.cleanupWorkspace(stale, now)
	current, _ := store.RunState(run.ID)
	if !current.WorkspaceCleanedAt.Equal(now) {
		t.Fatalf("previous cleanup changed the current claim: %v", current.WorkspaceCleanedAt)
	}
	token, err := store.BeginWorkspaceUse(run.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	e.cleanupWorkspace(current, now)
	current, _ = store.RunState(run.ID)
	if !current.WorkspaceCleanedAt.Equal(now) {
		t.Fatalf("cleanup changed the claim while workspace use was active: %v", current.WorkspaceCleanedAt)
	}
	if err := store.FinishWorkspaceUse(run.ID, token, now, nil, ""); err != nil {
		t.Fatal(err)
	}
	e.cleanupWorkspace(current, now)
	current, _ = store.RunState(run.ID)
	if !current.WorkspaceCleanedAt.IsZero() {
		t.Fatal("interrupted current cleanup was not reopened for retry")
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
		t.Fatal("completed SOP workspace was not reclaimed")
	}
	if _, err := os.Stat(e.RunWorkspace(r.ID)); !os.IsNotExist(err) {
		t.Fatalf("claimed checkout remained on disk: %v", err)
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

func TestCommittedCleanupClaimReclaimsCheckoutAfterRestart(t *testing.T) {
	t.Parallel()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	root := t.TempDir()
	run := domain.Run{ID: "crash-window-run", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	crashed := New(store, runtime.NewManager(root))
	checkout := crashed.RunWorkspace(run.ID)
	orphan := crashed.RunWorkspace("unregistered-orphan")
	for _, dir := range []string{checkout, orphan} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "big.bin"), make([]byte, 1<<20), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The claim commits, then the process stops before DestroyRun.
	if claimed, err := store.ClaimRunWorkspaceCleanup(run.ID, time.Now().UTC()); err != nil || !claimed {
		t.Fatalf("claim cleanup: %v %v", claimed, err)
	}

	restarted := New(store, runtime.NewManager(root))
	for range 3 {
		restarted.reconcileTerminalWorkspaceCleanup()
		restarted.reclaimSandboxes()
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("checkout_exists=%v after restart sweeps", err == nil)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("unregistered orphan remained: %v", err)
	}
}

func TestCleanedWorkspaceLeftoverKeepsCheckpointLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	for _, scenario := range []struct {
		name       string
		status     string
		cleaned    bool
		checkpoint *repo.WorkspaceCheckpoint
		lock       bool
		removed    bool
	}{
		{name: "legacy claim without retry", status: "succeeded", cleaned: true, removed: true,
			checkpoint: &repo.WorkspaceCheckpoint{SavedAt: &past, SHA256: "saved", LeaseToken: "old", LeaseUntil: &past}},
		{name: "uncleaned failed run", status: "failed"},
		{name: "cleaned run executing again", status: "running", cleaned: true},
		{name: "pending cleanup retry", status: "succeeded", cleaned: true,
			checkpoint: &repo.WorkspaceCheckpoint{SavedAt: &past, SHA256: "saved", CleanupRetryAt: &future}},
		{name: "active restore lease", status: "succeeded", cleaned: true,
			checkpoint: &repo.WorkspaceCheckpoint{SavedAt: &past, SHA256: "saved", LeaseToken: "restore", LeaseUntil: &future, RestoreStatus: "restoring"}},
		{name: "restore holding checkpoint lock", status: "succeeded", cleaned: true, lock: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			store, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
			e := New(store, runtime.NewManager(t.TempDir()))
			run := domain.Run{ID: "leftover-run", Status: scenario.status, Nodes: map[string]*domain.NodeRun{}}
			if scenario.cleaned {
				run.WorkspaceCleanedAt = past
			}
			if err := store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if scenario.checkpoint != nil {
				row := *scenario.checkpoint
				row.RunID, row.LastUsedAt = run.ID, past
				if err := store.DB.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
			checkout := e.RunWorkspace(run.ID)
			if err := os.MkdirAll(checkout, 0700); err != nil {
				t.Fatal(err)
			}
			if scenario.lock {
				release, err := e.tryLockCheckpointWorkspace(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			e.reconcileTerminalWorkspaceCleanup()
			e.reclaimSandboxes()
			_, err = os.Stat(checkout)
			if removed := os.IsNotExist(err); removed != scenario.removed {
				t.Fatalf("checkout removed=%v, want %v (%v)", removed, scenario.removed, err)
			}
		})
	}
}

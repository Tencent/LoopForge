package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const sopIdleFile = "from before reclaim"

func stoppedSOPForReclaim(t *testing.T, s *repo.Store, ec domain.TelemetryContext, status string, paused bool) domain.Run {
	t.Helper()
	check := `test "$(cat kept.txt)" = "` + sopIdleFile + `" && cat kept.txt`
	workflow := domain.WorkflowDefinition{
		ID: "idle-workflow", Name: "Idle workflow",
		// These fixed test commands only check a temporary file. The test is
		// about scheduling and restoration, independent of the sandbox driver.
		SandboxSnapshot: &domain.RunnerSandboxSpec{},
		Nodes: []domain.WorkflowNode{
			{ID: "work", Type: "script", ScriptLanguage: "shell", Script: check, MaxAttempts: 1},
			{ID: "finish", Type: "script", ScriptLanguage: "shell", Script: check, MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{Source: "work", Target: "finish", When: "success"}},
	}
	run := domain.Run{ID: "idle-sop", Status: "running", WorkflowID: workflow.ID, WorkflowSnapshot: workflow, ExecutionContext: ec,
		Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "failed", Attempt: 1}, "finish": {NodeID: "finish", Status: "pending"}},
	}
	if status == "waiting" {
		run.Nodes["work"].Status = "waiting_approval"
	}
	if paused {
		run.Nodes["work"].Activity = "已由用户暂停，继续后脚本将从头执行"
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if paused {
		if _, err := s.RequestRunPause(run.ID); err != nil {
			t.Fatal(err)
		}
	}
	run, _ = s.RunState(run.ID)
	run.Status = status
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	run, _ = s.RunState(run.ID)
	return run
}

func writeSOPIdleFile(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kept.txt"), []byte(sopIdleFile), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setSOPIdleTime(t *testing.T, s *repo.Store, run domain.Run, at time.Time) domain.Run {
	t.Helper()
	if err := s.DB.Table("runs").Where("id = ?", run.ID).Update("updated_at", at).Error; err != nil {
		t.Fatal(err)
	}
	run, _ = s.RunState(run.ID)
	return run
}

func putSOPSlotHolders(t *testing.T, s *repo.Store, ec domain.TelemetryContext, count int) func() {
	t.Helper()
	for i := range count {
		run := domain.Run{ID: fmt.Sprintf("slot-holder-%d", i), Status: "running", ExecutionContext: ec}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
		run, _ = s.RunState(run.ID)
		if run.Status != "running" {
			t.Fatalf("slot holder status = %s", run.Status)
		}
		run.Status = "waiting"
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		t.Helper()
		run, _ := s.RunState("slot-holder-0")
		run.Status = "succeeded"
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
}

func assertSOPReclaimed(t *testing.T, s *repo.Store, id string) domain.Run {
	t.Helper()
	run, _ := s.RunState(id)
	meta, _ := s.WorkspaceCheckpoint(id)
	if run.WorkspaceCleanedAt.IsZero() || run.AdmissionState != "released" || meta.SavedAt == nil || meta.Error != "" {
		t.Fatalf("reclaim: cleaned=%v, admission=%q, saved=%v, error=%q", run.WorkspaceCleanedAt, run.AdmissionState, meta.SavedAt, meta.Error)
	}
	if !slices.ContainsFunc(run.Events, func(event domain.RunEvent) bool { return event.Type == "workspace.reclaimed" }) {
		t.Fatal("missing workspace.reclaimed event")
	}
	return run
}

func TestSOPIdleReclaimReleasesSlotsAtOneHour(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, status string
		paused       bool
	}{{"failed", "failed", false}, {"approval", "waiting", false}, {"paused", "waiting", true}} {
		t.Run(tc.name, func(t *testing.T) {
			e, s := newRecoveryEngine(t)
			ec := domain.TelemetryContext{ProjectID: "idle", ExecutionMode: domain.ExecutionModeCloud}
			run := stoppedSOPForReclaim(t, s, ec, tc.status, tc.paused)
			root := e.RunWorkspace(run.ID)
			writeSOPIdleFile(t, root)
			putSOPSlotHolders(t, s, ec, repo.ProjectRunLimit-1)
			if err := s.PutRun(domain.Run{ID: "queued", Status: "running", ExecutionContext: ec}); err != nil {
				t.Fatal(err)
			}
			e.cleanupWorkspace(run, run.UpdatedAt.Add(ConversationWorkspaceIdleTTL-time.Second))
			if _, exists := s.WorkspaceCheckpoint(run.ID); exists {
				t.Fatal("a fresh run acquired a checkpoint lease")
			}
			if got, _ := s.RunState(run.ID); got.AdmissionState != "held" || !got.WorkspaceCleanedAt.IsZero() {
				t.Fatalf("a fresh run was reclaimed: admission=%q, cleaned=%v", got.AdmissionState, got.WorkspaceCleanedAt)
			}
			if promoted, err := s.PromoteQueuedRuns(); err != nil || len(promoted) != 0 {
				t.Fatalf("fresh SOP stopped holding its slot: %v, %v", promoted, err)
			}
			run = setSOPIdleTime(t, s, run, time.Now().UTC().Add(-ConversationWorkspaceIdleTTL))
			e.reconcileTerminalWorkspaceCleanup()
			got := assertSOPReclaimed(t, s, run.ID)
			if got.Status != tc.status || got.PauseRequested != tc.paused || !got.UpdatedAt.Equal(run.UpdatedAt) {
				t.Fatalf("reclaim changed the parked run: %s, pause=%v, updated=%v", got.Status, got.PauseRequested, got.UpdatedAt)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("workspace still present: %v", err)
			}
			if promoted, err := s.PromoteQueuedRuns(); err != nil || !slices.Equal(promoted, []string{"queued"}) {
				t.Fatalf("released slot did not promote its waiter: %v, %v", promoted, err)
			}
		})
	}
}

func TestSOPIdleReclaimTouchAndDescendantActivity(t *testing.T) {
	t.Parallel()
	e, s := newRecoveryEngine(t)
	run := stoppedSOPForReclaim(t, s, domain.TelemetryContext{ProjectID: "idle"}, "waiting", true)
	now := time.Now().UTC()
	writeSOPIdleFile(t, e.RunWorkspace(run.ID))
	child := domain.Run{ID: "child", Status: "waiting", ExecutionContext: domain.TelemetryContext{ParentRunID: run.ID, WorkspaceRunID: run.ID},
		Nodes: map[string]*domain.NodeRun{"nested": {ChildRunID: "grandchild", Status: "waiting_approval"}}}
	grandchild := domain.Run{ID: "grandchild", Status: "running", ExecutionContext: domain.TelemetryContext{ParentRunID: child.ID, WorkspaceRunID: run.ID}}
	for _, nested := range []domain.Run{child, grandchild} {
		if err := s.PutRun(nested); err != nil {
			t.Fatal(err)
		}
	}
	run.Nodes["work"].ChildRunID = child.ID
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	run = setSOPIdleTime(t, s, run, now.Add(-2*time.Hour))
	assertHeld := func() {
		t.Helper()
		e.cleanupWorkspace(run, now)
		if got, _ := s.RunState(run.ID); got.AdmissionState != "held" || !got.WorkspaceCleanedAt.IsZero() {
			t.Fatalf("a protected workspace was reclaimed: admission=%q, cleaned=%v", got.AdmissionState, got.WorkspaceCleanedAt)
		}
	}
	assertHeld() // running grandchild
	grandchild, _ = s.RunState(grandchild.ID)
	grandchild.Status = "waiting"
	grandchild.Nodes = map[string]*domain.NodeRun{"active": {Status: "pending_input"}}
	if err := s.PutRun(grandchild); err != nil {
		t.Fatal(err)
	}
	assertHeld() // active node on a waiting grandchild
	grandchild.Nodes["active"].Status = "waiting_approval"
	if err := s.PutRun(grandchild); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{ID: "child-task", Provider: "terminal", RunID: grandchild.ID, Status: domain.RunnerTaskQueued}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	assertHeld() // a queued Runner task still uses the shared workspace
	if err := s.CancelRunnerTask(task.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchWorkspaceUse(run.ID, now); err != nil {
		t.Fatal(err)
	}
	assertHeld()
	holder, err := e.sandboxes.AcquireRunIn(context.Background(), run.ID, run.ID, domain.SandboxProfile{}, runtime.SandboxRequirements{}, runtime.RunCredentials{}, runtime.SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close(context.Background()) })
	e.cleanupWorkspace(run, now.Add(ConversationWorkspaceIdleTTL))
	if got, _ := s.RunState(run.ID); got.AdmissionState != "held" || !got.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("a locked directory was reported as reclaimed: admission=%q, cleaned=%v", got.AdmissionState, got.WorkspaceCleanedAt)
	}
	if err := holder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.cleanupWorkspace(run, now.Add(ConversationWorkspaceIdleTTL))
	assertSOPReclaimed(t, s, run.ID)
}

func TestSOPIdleReclaimReconcilesAlreadyCleanedSlots(t *testing.T) {
	t.Parallel()
	e, s := newRecoveryEngine(t)
	for _, status := range []string{"cancelled", "failed", "waiting"} {
		run := domain.Run{ID: status, Status: status, WorkspaceCleanedAt: time.Now().UTC()}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	remote := domain.Run{ID: "runner-cleanup-failed", Status: "failed", WorkspaceCleanedAt: time.Now().UTC(),
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner"}}
	if err := s.PutRun(remote); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunnerTask(domain.RunnerTask{ID: "rt-cleanup-" + remote.ID, RunID: remote.ID, RunnerID: "runner", Provider: "terminal", Status: domain.RunnerTaskFailed}); err != nil {
		t.Fatal(err)
	}
	token, err := s.BeginWorkspaceCheckpointSave("waiting", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	if run, _ := s.RunState(remote.ID); !run.WorkspaceCleanedAt.IsZero() || run.AdmissionState == "released" {
		t.Fatalf("failed Runner cleanup must reset without releasing: %v, %q", run.WorkspaceCleanedAt, run.AdmissionState)
	}
	if run, _ := s.RunState("waiting"); run.AdmissionState == "released" {
		t.Fatal("another sweep released a slot before the cleanup lease ended")
	}
	if err := s.FinishWorkspaceCheckpointSave("waiting", token, time.Now().UTC(), time.Now().UTC(), ""); err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	for _, status := range []string{"cancelled", "failed", "waiting"} {
		run, _ := s.RunState(status)
		if run.Status != status || run.AdmissionState != "released" || len(run.Events) != 0 {
			t.Fatalf("run %s did not reconcile idempotently: admission=%q, events=%+v", run.ID, run.AdmissionState, run.Events)
		}
	}
}

func TestSOPIdleReclaimOnRunnerSavesBeforeSchedulingCleanup(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"with files", "unmaterialized", "offline", "missing entry", "queue failure"} {
		t.Run(scenario, func(t *testing.T) {
			e, s := newRecoveryEngine(t)
			runner := domain.Runner{ID: "runner", OwnerUserID: 1, Status: domain.RunnerStatusOnline}
			if scenario == "offline" {
				runner.Status = domain.RunnerStatusOffline
			}
			if err := s.PutRunner(runner); err != nil {
				t.Fatal(err)
			}
			ec := domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID}
			run := stoppedSOPForReclaim(t, s, ec, "failed", false)
			now := time.Now().UTC()
			run = setSOPIdleTime(t, s, run, now.Add(-2*time.Hour))
			root := filepath.Join(t.TempDir(), "runner-workspace")
			if scenario != "unmaterialized" {
				writeSOPIdleFile(t, root)
			}
			if scenario == "queue failure" {
				if err := s.DB.Exec(`CREATE TRIGGER reject_cleanup BEFORE INSERT ON runner_tasks BEGIN SELECT RAISE(ABORT, 'queue unavailable'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			transfers := &runtime.CheckpointTransfers{}
			t.Cleanup(transfers.Close)
			exports := 0
			e.WorkspaceCheckpointTransport = func(ctx context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				if req.Operation == domain.RunnerWorkspaceOperationCheckpointStatus {
					return runtime.WorkspaceCheckpointStatus(root), nil
				}
				if req.Operation == domain.RunnerWorkspaceOperationCheckpointExport {
					exports++
					if scenario == "offline" {
						return domain.RunnerWorkspaceResponse{}, errors.New("runner offline")
					}
					if scenario == "missing entry" {
						return domain.RunnerWorkspaceResponse{Status: 404, Code: domain.WorkspaceErrorMissing, Error: "a file disappeared during export"}, nil
					}
				}
				return transfers.Handle(ctx, root, req), nil
			}
			e.cleanupWorkspace(run, now)
			task, exists := s.RunnerTask("rt-cleanup-" + run.ID)
			if scenario == "with files" || scenario == "unmaterialized" {
				assertSOPReclaimed(t, s, run.ID)
				if !exists || task.Status != domain.RunnerTaskQueued || task.Action != domain.RunnerTaskActionCleanupRun || task.RunnerID != runner.ID {
					t.Fatalf("runner cleanup was not queued: %+v", task)
				}
				archive, err := s.WorkspaceCheckpointArchive(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				restored := filepath.Join(t.TempDir(), "restored")
				if err := runtime.RestoreWorkspaceCheckpoint(restored, archive); err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(filepath.Join(restored, "kept.txt"))
				if scenario == "with files" && (err != nil || string(content) != sopIdleFile) || scenario == "unmaterialized" && !os.IsNotExist(err) {
					t.Fatalf("unexpected runner checkpoint contents: %q, %v", content, err)
				}
				return
			}
			got, _ := s.RunState(run.ID)
			if exists || got.AdmissionState != "held" || !got.WorkspaceCleanedAt.IsZero() {
				t.Fatalf("failed cleanup freed the slot: admission=%q, cleaned=%v, task=%+v", got.AdmissionState, got.WorkspaceCleanedAt, task)
			}
			if contents, err := os.ReadFile(filepath.Join(root, "kept.txt")); err != nil || string(contents) != sopIdleFile {
				t.Fatalf("failed cleanup lost files: %q, %v", contents, err)
			}
			meta, _ := s.WorkspaceCheckpoint(run.ID)
			if scenario == "queue failure" {
				if meta.SavedAt == nil {
					t.Fatal("failed to save files before scheduling cleanup")
				}
				return
			}
			if meta.Error == "" || meta.SaveRetryAt == nil || meta.SavedAt != nil {
				t.Fatalf("checkpoint did not back off after export error: %+v", meta)
			}
			e.cleanupWorkspace(run, now.Add(time.Second))
			if exports != 1 {
				t.Fatalf("checkpoint exported %d times during backoff", exports)
			}
		})
	}
}

func TestSOPIdleReclaimBorrowedAdmissionLetsOwnerReclaim(t *testing.T) {
	t.Parallel()
	e, s := newRecoveryEngine(t)
	now := time.Now().UTC()
	ec := domain.TelemetryContext{ProjectID: "borrowed", ExecutionMode: domain.ExecutionModeCloud}
	owner := domain.Run{ID: "conversation", Kind: domain.RunKindWorkspace, Status: "waiting", ExecutionContext: ec}
	if err := s.PutRun(owner); err != nil {
		t.Fatal(err)
	}
	owner = setSOPIdleTime(t, s, owner, now.Add(-2*time.Hour))
	writeSOPIdleFile(t, e.RunWorkspace(owner.ID))
	if err := s.TouchWorkspaceUse(owner.ID, owner.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	ec.WorkspaceRunID, ec.ExecutionPinned = owner.ID, true
	run := stoppedSOPForReclaim(t, s, ec, "waiting", false)
	child := domain.Run{ID: "passive-child", Status: "waiting", ExecutionContext: ec}
	child.ExecutionContext.ParentRunID = run.ID
	if err := s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	run.Nodes["work"].ChildRunID = child.ID
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	run = setSOPIdleTime(t, s, run, now.Add(-2*time.Hour))
	e.cleanupWorkspace(owner, now)
	if got, _ := s.RunState(owner.ID); !got.WorkspaceCleanedAt.IsZero() {
		t.Fatal("owner reclaimed while its borrower was waiting with a slot")
	}
	e.cleanupWorkspace(run, now)
	if got, _ := s.RunState(run.ID); got.AdmissionState != "released" || !got.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("borrower should only release its slot: admission=%q, cleaned=%v", got.AdmissionState, got.WorkspaceCleanedAt)
	}
	if _, exists := s.WorkspaceCheckpoint(run.ID); exists {
		t.Fatal("borrower got its own checkpoint")
	}
	if got, _ := s.RunState(owner.ID); !got.WorkspaceCleanedAt.IsZero() {
		t.Fatal("borrower cleaned the conversation checkout")
	}
	if _, err := os.Stat(filepath.Join(e.RunWorkspace(owner.ID), "kept.txt")); err != nil {
		t.Fatal(err)
	}
	e.cleanupWorkspace(owner, now)
	if got, _ := s.RunState(owner.ID); got.WorkspaceCleanedAt.IsZero() {
		t.Fatal("released borrower still blocked the owner from reclaiming")
	}
	if err := e.Approve(run.ID, "work", ""); err != nil {
		t.Fatal(err)
	}
	assertSOPRestoredAndSucceeded(t, s, run.ID)
	if got, _ := s.RunState(owner.ID); !got.WorkspaceCleanedAt.IsZero() {
		t.Fatal("SOP did not restore the conversation checkout before execution")
	}
}

func assertSOPRestoredAndSucceeded(t *testing.T, s *repo.Store, id string) domain.Run {
	t.Helper()
	got := waitRun(t, s, id, 15*time.Second, func(run domain.Run) bool { return domain.TerminalRunStatus(run.Status) })
	final := got.Nodes["finish"]
	if got.Status != "succeeded" || final == nil || strings.TrimSpace(final.Output) != sopIdleFile {
		t.Fatalf("run %s after restoration: status=%s, work=%+v, finish=%+v", id, got.Status, got.Nodes["work"], final)
	}
	return got
}

func TestSOPIdleReclaimControlsRestoreWhenAdmitted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, status string
		paused, full bool
		activate     func(*Engine, string) error
	}{
		{"retry with free slot", "failed", false, false, func(e *Engine, id string) error { return e.Retry(id, "", "", "") }},
		{"retry", "failed", false, true, func(e *Engine, id string) error { return e.Retry(id, "", "", "") }},
		{"resume", "waiting", true, true, func(e *Engine, id string) error { return e.ResumePaused(id, "") }},
		{"approve", "waiting", false, true, func(e *Engine, id string) error { return e.Approve(id, "work", "") }},
		{"approve with free slot", "waiting", false, false, func(e *Engine, id string) error { return e.Approve(id, "work", "") }},
		{"pass", "failed", false, true, func(e *Engine, id string) error { return e.Pass(id, "work", "") }},
		{"pass with free slot", "failed", false, false, func(e *Engine, id string) error { return e.Pass(id, "work", "") }},
		{"reject", "waiting", false, true, func(e *Engine, id string) error {
			return e.Reject(id, domain.RejectRequest{NodeID: "work", TargetID: "work"})
		}},
		{"reject with free slot", "waiting", false, false, func(e *Engine, id string) error {
			return e.Reject(id, domain.RejectRequest{NodeID: "work", TargetID: "work"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, s := newRecoveryEngine(t)
			ec := domain.TelemetryContext{ProjectID: "idle", ExecutionMode: domain.ExecutionModeCloud}
			run := stoppedSOPForReclaim(t, s, ec, tc.status, tc.paused)
			root := e.RunWorkspace(run.ID)
			writeSOPIdleFile(t, root)
			e.cleanupWorkspace(run, run.UpdatedAt.Add(ConversationWorkspaceIdleTTL))
			run = assertSOPReclaimed(t, s, run.ID)
			var freeSlot func()
			if tc.full {
				freeSlot = putSOPSlotHolders(t, s, ec, repo.ProjectRunLimit)
			}
			if err := tc.activate(e, run.ID); err != nil {
				t.Fatal(err)
			}
			if tc.full {
				if got, _ := s.RunState(run.ID); got.Status != "queued" || got.AdmissionState != "queued" || !got.WorkspaceCleanedAt.Equal(run.WorkspaceCleanedAt) {
					t.Fatalf("queued run changed its archive marker: %s, %s, %v", got.Status, got.AdmissionState, got.WorkspaceCleanedAt)
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("queue opened the workspace before admission: %v", err)
				}
				freeSlot()
				if resumed := e.ResumeInterruptedRuns(); !slices.Contains(resumed, run.ID) {
					t.Fatalf("worker did not start promoted SOP: %v", resumed)
				}
			}
			assertSOPRestoredAndSucceeded(t, s, run.ID)
		})
	}
}

func TestSOPIdleReclaimResumesPausedChildAfterParentAdmission(t *testing.T) {
	t.Parallel()
	e, s := newRecoveryEngine(t)
	ec := domain.TelemetryContext{ProjectID: "idle", ExecutionMode: domain.ExecutionModeCloud}
	parent := stoppedSOPForReclaim(t, s, ec, "waiting", true)
	child := domain.Run{ID: "paused-child", WorkflowID: "nested-sop", Status: "running", ExecutionContext: ec,
		WorkflowSnapshot: parent.WorkflowSnapshot,
		Nodes:            map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "waiting_approval", Activity: "已由用户暂停，继续后脚本将从头执行"}, "finish": {NodeID: "finish", Status: "pending"}},
	}
	child.WorkflowSnapshot.ID = child.WorkflowID
	child.ExecutionContext.ParentRunID, child.ExecutionContext.WorkspaceRunID = parent.ID, parent.ID
	if err := s.PutWorkflow(child.WorkflowSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestRunPause(child.ID); err != nil {
		t.Fatal(err)
	}
	child, _ = s.RunState(child.ID)
	child.Status = "waiting"
	if err := s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	parent.WorkflowSnapshot.Nodes[0] = domain.WorkflowNode{ID: "work", Type: "sop", SOPWorkflowID: child.WorkflowID, MaxAttempts: 1}
	parent.Nodes["work"].ChildRunID = child.ID
	if err := s.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	parent, _ = s.RunState(parent.ID)
	writeSOPIdleFile(t, e.RunWorkspace(parent.ID))
	e.cleanupWorkspace(parent, parent.UpdatedAt.Add(ConversationWorkspaceIdleTTL))
	assertSOPReclaimed(t, s, parent.ID)
	freeSlot := putSOPSlotHolders(t, s, ec, repo.ProjectRunLimit)
	if err := e.ResumePaused(parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RunState(parent.ID); got.Status != "queued" || got.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("parent did not queue with archive intact: %s, %v", got.Status, got.WorkspaceCleanedAt)
	}
	if got, _ := s.RunState(child.ID); got.Status != "waiting" || !got.PauseRequested {
		t.Fatalf("child resumed before its parent was admitted: %s, %v", got.Status, got.PauseRequested)
	}
	freeSlot()
	e.ResumeInterruptedRuns()
	got := assertSOPRestoredAndSucceeded(t, s, parent.ID)
	if got.Nodes["work"].ChildRunID != child.ID {
		t.Fatal("parent started a new child instead of resuming the paused child")
	}
	assertSOPRestoredAndSucceeded(t, s, child.ID)
}

func TestSOPIdleReclaimPromotedRunnerRestoresBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, status, cleanupStatus string
		paused                      bool
		activate                    func(*Engine, string) error
	}{
		{"approve with queued cleanup", "waiting", domain.RunnerTaskQueued, false, func(e *Engine, id string) error { return e.Approve(id, "work", "") }},
		{"retry after cleanup", "failed", domain.RunnerTaskSuccess, false, func(e *Engine, id string) error { return e.Retry(id, "", "", "") }},
		{"resume after cleanup", "waiting", domain.RunnerTaskSuccess, true, func(e *Engine, id string) error { return e.ResumePaused(id, "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker, s := newRecoveryEngine(t)
			api := New(s, runtime.NewManager(t.TempDir()))
			api.controlToken = "test-workspace-control"
			worker.WorkspaceCheckpointTransport = func(ctx context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				// Exercise the split API/worker guard without a listening socket.
				req.CheckpointLease = ConversationWorkspaceToken(ctx, req.RunID)
				body, err := json.Marshal(req)
				if err != nil {
					return domain.RunnerWorkspaceResponse{}, err
				}
				request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/internal/workspace-checkpoint", bytes.NewReader(body))
				request.Header.Set(controlTokenHeader, api.controlToken)
				response := httptest.NewRecorder()
				api.HandleWorkspaceCheckpoint(response, request)
				var result domain.RunnerWorkspaceResponse
				if response.Code != http.StatusOK {
					return result, fmt.Errorf("workspace RPC returned %d: %s", response.Code, response.Body)
				}
				err = json.Unmarshal(response.Body.Bytes(), &result)
				return result, err
			}
			runner := domain.Runner{ID: "runner", OwnerUserID: 1, Status: domain.RunnerStatusOnline,
				Runtimes: []domain.RunnerRuntime{{Provider: "terminal", Status: domain.RuntimeStatusOnline}}}
			if err := s.PutRunner(runner); err != nil {
				t.Fatal(err)
			}
			if err := s.SetRunnerRunLimit(runner.ID, 1); err != nil {
				t.Fatal(err)
			}
			ec := domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID, RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared}
			run := stoppedSOPForReclaim(t, s, ec, tc.status, tc.paused)
			root := filepath.Join(t.TempDir(), "runner-workspace")
			writeSOPIdleFile(t, root)
			api.WorkspaceCheckpointTransport = func(ctx context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				switch req.Operation {
				case domain.RunnerWorkspaceOperationCheckpointStatus:
					return runtime.WorkspaceCheckpointStatus(root), nil
				case domain.RunnerWorkspaceOperationRestore:
					return domain.RunnerWorkspaceResponse{Status: http.StatusOK}, runtime.RestoreWorkspaceCheckpoint(root, req.Checkpoint)
				default:
					return api.checkpointTransfers.Handle(ctx, root, req), nil
				}
			}
			t.Cleanup(api.checkpointTransfers.Close)
			worker.cleanupWorkspace(run, run.UpdatedAt.Add(ConversationWorkspaceIdleTTL))
			assertSOPReclaimed(t, s, run.ID)
			cleanupID := "rt-cleanup-" + run.ID
			if tc.cleanupStatus == domain.RunnerTaskSuccess {
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
				task, _ := s.RunnerTask(cleanupID)
				task.Status = tc.cleanupStatus
				if err := s.PutRunnerTask(task); err != nil {
					t.Fatal(err)
				}
			}
			freeSlot := putSOPSlotHolders(t, s, ec, 1)
			if err := tc.activate(worker, run.ID); err != nil {
				t.Fatal(err)
			}
			if got, _ := s.RunState(run.ID); got.Status != "queued" || got.WorkspaceCleanedAt.IsZero() {
				t.Fatalf("Runner activation restored before admission: status=%s, cleaned=%v", got.Status, got.WorkspaceCleanedAt)
			}
			if task, _ := s.RunnerTask(cleanupID); task.Status != tc.cleanupStatus {
				t.Fatalf("admission queue changed Runner cleanup: %s", task.Status)
			}
			worker.SetRunnerDispatchNotify(func(provider string) {
				for _, queued := range s.RunnerTasks() {
					if queued.RunID != run.ID || queued.Status != domain.RunnerTaskQueued || queued.NodeID == "" {
						continue
					}
					current, _ := s.RunState(run.ID)
					if !current.WorkspaceCleanedAt.IsZero() {
						t.Error("Runner script dispatched before clearing the cleanup marker")
					}
					cleanup, _ := s.RunnerTask(cleanupID)
					if tc.cleanupStatus == domain.RunnerTaskQueued && cleanup.Status != domain.RunnerTaskCanceled {
						t.Errorf("stale Runner cleanup task status = %s", cleanup.Status)
					}
					task, ok, err := s.ClaimRunnerTask(runner.ID, []string{provider}, time.Now().UTC(), time.Minute)
					if err != nil || !ok || task.ID != queued.ID {
						t.Errorf("claim script task: %s, %v, %v", task.ID, ok, err)
						return
					}
					cmd := exec.Command("sh", "-c", task.Command)
					cmd.Dir = root
					output, err := cmd.CombinedOutput()
					status, message := domain.RunnerTaskSuccess, ""
					if err != nil {
						status, message = domain.RunnerTaskFailed, err.Error()
					}
					if err := s.CompleteRunnerTask(task.ID, runner.ID, status, nil, string(output), message, nil, nil, time.Now().UTC()); err != nil {
						t.Error(err)
					}
				}
			})
			freeSlot()
			if resumed := worker.ResumeInterruptedRuns(); !slices.Contains(resumed, run.ID) {
				t.Fatalf("worker did not start promoted Runner SOP: %v", resumed)
			}
			assertSOPRestoredAndSucceeded(t, s, run.ID)
		})
	}
}

func TestSOPIdleReclaimRestoresOnAReplacementCloudWorker(t *testing.T) {
	t.Parallel()
	workerA, s := newRecoveryEngine(t)
	workerA.ConfigureExecution("worker-a", "worker-a-host", time.Minute, true)
	heartbeatHost(t, s, "worker-a", workerA.workspaceHost)
	workerB := New(s, runtime.NewManager(t.TempDir()))
	workerB.ConfigureExecution("worker-b", "worker-b-host", time.Minute, true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = workerB.Drain(ctx)
	})
	ec := domain.TelemetryContext{ProjectID: "cloud", ExecutionMode: domain.ExecutionModeCloud}
	run := stoppedSOPForReclaim(t, s, ec, "waiting", false)
	remote := t.TempDir()
	gitInit(t, remote)
	gitCommitFile(t, remote, "README.md", "base repository")
	run.Repository = domain.RepositoryContext{RepositoryID: "cloud-code", LocalPath: remote, BaseBranch: "master"}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Table("runs").Where("id = ?", run.ID).Updates(map[string]any{"workspace_host": workerA.workspaceHost, "workspace_root": workerA.sandboxes.Root()}).Error; err != nil {
		t.Fatal(err)
	}
	run, _ = s.RunState(run.ID)
	root := workerA.RunWorkspace(run.ID)
	if err := prepareRepositories(context.Background(), root, run.RepositoryContexts()); err != nil {
		t.Fatal(err)
	}
	writeSOPIdleFile(t, root)
	workerA.cleanupWorkspace(run, run.UpdatedAt.Add(ConversationWorkspaceIdleTTL))
	reclaimed := assertSOPReclaimed(t, s, run.ID)
	if err := s.DeleteEngineInstance("worker-a"); err != nil {
		t.Fatal(err)
	}
	if workerB.workspaceLost(reclaimed) {
		t.Fatal("worker marked a durably archived code workspace as lost")
	}
	freeSlot := putSOPSlotHolders(t, s, ec, repo.ProjectRunLimit)
	if err := workerB.Approve(run.ID, "work", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RunState(run.ID); got.Status != "queued" || got.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("cloud run did not queue with its archive intact: status=%s, cleaned=%v", got.Status, got.WorkspaceCleanedAt)
	}
	freeSlot()
	if resumed := workerB.ResumeInterruptedRuns(); !slices.Contains(resumed, run.ID) {
		t.Fatalf("replacement worker did not start promoted SOP: %v", resumed)
	}
	got := assertSOPRestoredAndSucceeded(t, s, run.ID)
	if got.WorkspaceHost != workerB.workspaceHost || got.WorkspaceRoot != workerB.sandboxes.Root() {
		t.Fatalf("restored workspace binding = %s:%s", got.WorkspaceHost, got.WorkspaceRoot)
	}
}

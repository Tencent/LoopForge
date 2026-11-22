package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestConversationWorkspaceCleanupHoldKeepsCurrentCheckpointUntilReclaim(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	if err := e.repo.SetRunWorkspaceCleanupHold(id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// An unsaved workspace still gets its first checkpoint during the hold.
	e.reconcileTerminalWorkspaceCleanup()
	saved, ok := e.repo.WorkspaceCheckpoint(id)
	if !ok || saved.SavedAt == nil || saved.SHA256 == "" || saved.Error != "" {
		t.Fatalf("hold prevented first checkpoint: %+v", saved)
	}
	path := filepath.Join(e.RunWorkspace(id), "README.md")
	if err := os.WriteFile(path, []byte("edit before release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.ReleaseConversationWorkspace(id); err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	released, _ := e.repo.WorkspaceCheckpoint(id)
	if released.SavedAt == nil || released.SHA256 == saved.SHA256 || released.Error != "" {
		t.Fatal("first checkpoint after release did not persist the edit")
	}
	saved = released
	if err := os.WriteFile(path, []byte("edit during cleanup hold"), 0600); err != nil {
		t.Fatal(err)
	}

	// A fresh maintenance instance must reuse the persisted checkpoint until
	// cleanup can go ahead. It must still capture the on-disk files before deletion.
	e = New(e.repo, e.sandboxes)
	for range 3 {
		e.reconcileTerminalWorkspaceCleanup()
		current, _ := e.repo.WorkspaceCheckpoint(id)
		if current.SavedAt == nil || !current.SavedAt.Equal(*saved.SavedAt) || current.SHA256 != saved.SHA256 {
			t.Fatal("maintenance replaced a current checkpoint during the hold")
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("held workspace was removed", err)
	}
	if err := e.repo.ClearRunWorkspaceCleanupHold(id); err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	run, _ := e.repo.RunState(id)
	if run.WorkspaceCleanedAt.IsZero() {
		t.Fatal("workspace was not reclaimed after the hold cleared")
	}
	ctx := context.Background()
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnsureConversationWorkspace(ctx, id, token); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "edit during cleanup hold" {
		t.Fatalf("final checkpoint lost the latest file: %q, %v", content, err)
	}
	if err := e.EndConversationWorkspaceUse(ctx, id, token); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointCleanupHoldSavesDueRetry(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	now := time.Now().UTC()
	if err := e.repo.SetRunWorkspaceCleanupHold(id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	saved, _ := e.repo.WorkspaceCheckpoint(id)
	if saved.SavedAt == nil {
		t.Fatal("missing initial checkpoint")
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.repo.MarkWorkspaceFollowUp(id, token, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.RunWorkspace(id), "README.md")
	if err := os.WriteFile(path, []byte("edit from failed save"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.FinishWorkspaceUse(id, token, time.Now().UTC(), nil, "checkpoint interrupted"); err != nil {
		t.Fatal(err)
	}
	pending, _ := e.repo.WorkspaceCheckpoint(id)
	if pending.SaveRetryAt == nil {
		t.Fatal("retry is not scheduled")
	}
	run, _ := e.repo.RunState(id)
	e.cleanupWorkspace(run, pending.SaveRetryAt.Add(-time.Second))
	current, _ := e.repo.WorkspaceCheckpoint(id)
	if current.SHA256 != saved.SHA256 || current.SaveRetryAt == nil {
		t.Fatal("held workspace retried before its deadline")
	}
	e.cleanupWorkspace(run, *pending.SaveRetryAt)
	current, _ = e.repo.WorkspaceCheckpoint(id)
	if current.SHA256 == saved.SHA256 || current.SaveRetryAt != nil || current.Error != "" {
		t.Fatalf("hold prevented checkpoint retry: %+v", current)
	}
	run, _ = e.repo.RunState(id)
	if !run.WorkspaceCleanedAt.IsZero() {
		t.Fatal("retry reclaimed a held workspace")
	}
}

func TestConversationWorkspaceStaleCleanupKeepsReclaimedCheckpoint(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	if err := e.ReleaseConversationWorkspace(id); err != nil {
		t.Fatal(err)
	}
	staleRun, _ := e.repo.RunState(id)
	e.cleanupWorkspace(staleRun, time.Now().UTC())
	saved, _ := e.repo.WorkspaceCheckpoint(id)
	cleaned, _ := e.repo.RunState(id)
	if cleaned.WorkspaceCleanedAt.IsZero() || saved.SavedAt == nil {
		t.Fatal("first sweep did not save and reclaim the workspace")
	}

	// A second replica still holds the unreclaimed run from its sweep. The
	// local path is gone, so capturing again could publish an empty archive.
	e = New(e.repo, e.sandboxes)
	e.cleanupWorkspace(staleRun, time.Now().UTC())
	current, _ := e.repo.WorkspaceCheckpoint(id)
	if current.SHA256 != saved.SHA256 || current.Error != "" || current.SaveRetryAt != nil {
		t.Fatal("a stale sweep changed the reclaimed workspace checkpoint")
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnsureConversationWorkspace(t.Context(), id, token); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(e.RunWorkspace(id), "README.md"))
	if err != nil || string(content) != "uncommitted" {
		t.Fatalf("restored checkpoint did not preserve the original file: %q, %v", content, err)
	}
}

func TestConversationWorkspaceReclaimRetryKeepsVerifiedArchive(t *testing.T) {
	t.Parallel()
	worker, id := checkpointFixture(t)
	worker.workspaceHost = "checkpoint-retry-worker"
	worker.controlToken = "checkpoint-retry-test"
	if err := worker.repo.DB.Table("runs").Where("id = ?", id).Updates(map[string]any{
		"workspace_host": worker.workspaceHost,
		"workspace_root": worker.sandboxes.Root(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	var exports, reclaims atomic.Int64
	var dropReply atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		var req domain.RunnerWorkspaceRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		switch req.Operation {
		case domain.RunnerWorkspaceOperationCheckpointExport:
			exports.Add(1)
		case domain.RunnerWorkspaceOperationReclaim:
			reclaims.Add(1)
			if dropReply.Load() {
				// The Worker deletes the checkout, but the controller loses
				// the result and must probe the actual directory.
				worker.HandleWorkspaceCheckpoint(httptest.NewRecorder(), r)
			}
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		worker.HandleWorkspaceCheckpoint(w, r)
	}))
	defer server.Close()
	t.Cleanup(worker.checkpointTransfers.Close)
	if err := worker.repo.HeartbeatEngineInstance(repo.EngineInstance{
		InstanceID: worker.workspaceHost, Role: "worker", WorkspaceHost: worker.workspaceHost, ControlURL: server.URL,
	}, time.Minute); err != nil {
		t.Fatal(err)
	}
	controller := func() *Engine {
		e := New(worker.repo, runtime.NewManager(t.TempDir()))
		e.workspaceHost, e.controlToken = "checkpoint-retry-controller", worker.controlToken
		return e
	}
	e := controller()
	if err := e.ReleaseConversationWorkspace(id); err != nil {
		t.Fatal(err)
	}
	sweep := func(now time.Time) {
		t.Helper()
		run, ok := e.repo.RunState(id)
		if !ok {
			t.Fatal("workspace disappeared")
		}
		e.cleanupWorkspace(run, now)
	}
	sweep(time.Now().UTC())
	first, _ := e.repo.WorkspaceCheckpoint(id)
	if first.CleanupRetryAt == nil || first.ArchiveID == "" || reclaims.Load() != 1 {
		t.Fatal("failed reclaim did not retain a checkpoint and schedule its retry")
	}
	e = controller()
	sweep(first.CleanupRetryAt.Add(-time.Second))
	if exports.Load() != 1 || reclaims.Load() != 1 {
		t.Fatal("a new controller attempted cleanup before the stored deadline")
	}
	sweep(*first.CleanupRetryAt)
	second, _ := e.repo.WorkspaceCheckpoint(id)
	if exports.Load() != 2 || reclaims.Load() != 2 || second.ArchiveID != first.ArchiveID ||
		second.CleanupRetryAt == nil || !second.CleanupRetryAt.After(*first.CleanupRetryAt) {
		t.Fatal("unchanged retry did not reuse the verified archive and defer again")
	}
	path := filepath.Join(worker.RunWorkspace(id), "README.md")
	if err := os.WriteFile(path, []byte("edited during failed cleanup"), 0600); err != nil {
		t.Fatal(err)
	}
	dropReply.Store(true)
	sweep(*second.CleanupRetryAt)
	done, _ := e.repo.RunState(id)
	if done.WorkspaceCleanedAt.IsZero() {
		t.Fatal("lost reclaim reply made the successfully cleaned workspace unavailable for restore")
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnsureConversationWorkspace(t.Context(), id, token); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "edited during failed cleanup" {
		t.Fatalf("restore lost the edit: %q, %v", content, err)
	}
	restored, _ := e.repo.WorkspaceCheckpoint(id)
	if restored.ArchiveID == first.ArchiveID || restored.CleanupRetryAt != nil {
		t.Fatal("restore did not retain the latest archive or reset the cleanup deadline")
	}
}

func TestBackgroundWorkspaceSaveRecoversFromSlowRunnerAndExpiredLease(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run, _ := e.repo.RunState(id)
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
	run.ExecutionContext.ExecutionRunnerID = "test-runner"
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	archive, err := runtime.CreateWorkspaceCheckpoint(e.RunWorkspace(id))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string][]byte{"checkpoint": archive})
	if err != nil {
		t.Fatal(err)
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	busyUntil := time.Now().Add(6 * time.Second)
	var calls atomic.Int64
	var captures atomic.Int64
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if req.Operation != domain.RunnerWorkspaceOperationCheckpointExport {
			t.Errorf("unexpected checkpoint operation %s", req.Operation)
			return domain.RunnerWorkspaceResponse{Status: 500}, nil
		}
		calls.Add(1)
		if time.Now().Before(busyUntil) {
			return domain.RunnerWorkspaceResponse{Code: domain.WorkspaceErrorBusy, Status: 409}, nil
		}
		if captures.Add(1) == 1 {
			// The first transfer finishes after the lease expires.
			if err := e.repo.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", id).Update("lease_until", time.Now().UTC().Add(-time.Second)).Error; err != nil {
				return domain.RunnerWorkspaceResponse{}, err
			}
		}
		return domain.RunnerWorkspaceResponse{Status: 200, Payload: payload}, nil
	}
	done, err := e.EndConversationWorkspaceUseAsync(context.Background(), id, token)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(12 * time.Second):
		meta, _ := e.repo.WorkspaceCheckpoint(id)
		if cancel, ok := e.conversationSaves.Load(meta.LeaseToken); ok {
			cancel.(context.CancelFunc)()
			<-done
		}
		t.Fatal("background save did not recover")
	}

	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if meta.SavedAt == nil || meta.Error != "" || meta.SaveRetryAt != nil || meta.LeaseToken != "" || captures.Load() != 2 {
		t.Fatalf("retry did not publish exactly once: captures=%d, checkpoint=%+v", captures.Load(), meta)
	}
	if count := calls.Load(); count > 20 {
		t.Fatalf("runner polled too frequently: %d attempts in six seconds", count)
	}
	got, err := e.repo.WorkspaceCheckpointArchive(id)
	if err != nil || string(got) != string(archive) {
		t.Fatal("published archive changed", err)
	}
}

func TestHeldCheckpointSavesOnceAtIdleDeadline(t *testing.T) {
	idle := time.Now().UTC()
	deadline := idle.Add(ConversationWorkspaceIdleTTL)
	earlier := idle.Add(time.Minute)
	meta := repo.WorkspaceCheckpoint{SavedAt: &earlier, SHA256: "saved"}
	if heldWorkspaceCheckpointNeedsSave(meta, idle, deadline.Add(-time.Second)) {
		t.Fatal("current checkpoint should be kept before idle expiry")
	}
	if !heldWorkspaceCheckpointNeedsSave(meta, idle, deadline) {
		t.Fatal("first checkpoint at idle expiry must include late edits")
	}
	meta.SavedAt = &deadline
	if heldWorkspaceCheckpointNeedsSave(meta, idle, deadline.Add(time.Second)) {
		t.Fatal("checkpoint already taken at idle expiry should be kept")
	}
}

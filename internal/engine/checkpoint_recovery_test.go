package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestTerminalWorkspaceFinalCaptureAfterCleanupHold(t *testing.T) {
	t.Parallel()
	for _, removeRoot := range []bool{false, true} {
		name := "late edit"
		if removeRoot {
			name = "missing unbound checkout"
		}
		t.Run(name, func(t *testing.T) {
			e, id := checkpointFixture(t)
			if err := e.repo.SetRunWorkspaceCleanupHold(id, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			e.cleanupRunWorkspace(id)
			first, _ := e.repo.WorkspaceCheckpoint(id)
			if first.ArchiveID == "" {
				t.Fatal("held terminal workspace was not saved")
			}
			root := e.RunWorkspace(id)
			path := filepath.Join(root, "README.md")
			want := "uncommitted"
			if removeRoot {
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
			} else {
				want = "edit during a preview hold"
				if err := os.WriteFile(path, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.repo.ClearRunWorkspaceCleanupHold(id); err != nil {
				t.Fatal(err)
			}
			e.cleanupRunWorkspace(id)
			current, _ := e.repo.WorkspaceCheckpoint(id)
			if (current.ArchiveID == first.ArchiveID) != removeRoot {
				t.Fatal("final capture did not preserve the latest available files")
			}
			token, err := e.BeginConversationWorkspaceUse(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.EnsureConversationWorkspace(t.Context(), id, token); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != want {
				t.Fatalf("restored file: got %q, want %q, error %v", content, want, err)
			}
		})
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

func TestStaleWorkspaceSweepCannotReplaceCheckpointAfterRebind(t *testing.T) {
	t.Parallel()
	oldOwner, id := checkpointFixture(t)
	stale, _ := oldOwner.repo.RunState(id)
	newOwner := New(oldOwner.repo, runtime.NewManager(sandboxRoot(t)))
	newOwner.workspaceHost = "new-checkpoint-owner"
	newRoot := newOwner.RunWorkspace(id)
	if err := os.MkdirAll(newRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newRoot, "README.md"), []byte("new owner edits"), 0600); err != nil {
		t.Fatal(err)
	}
	token, err := newOwner.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := newOwner.repo.RestoreCheckpointWorkspaceBinding(id, token, newOwner.workspaceHost, newOwner.sandboxes.Root(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := newOwner.EndConversationWorkspaceUse(t.Context(), id, token); err != nil {
		t.Fatal(err)
	}
	saved, _ := newOwner.repo.WorkspaceCheckpoint(id)
	if saved.ArchiveID == "" {
		t.Fatal("new owner did not publish a checkpoint")
	}

	oldOwner.cleanupWorkspace(stale, time.Now().UTC())
	meta, _ := newOwner.repo.WorkspaceCheckpoint(id)
	run, _ := newOwner.repo.RunState(id)
	if meta.ArchiveID != saved.ArchiveID || !run.WorkspaceCleanedAt.IsZero() || meta.Error != "" {
		t.Fatal("a sweep with an old run revision modified the new owner's checkpoint")
	}
}

func remoteWorkspaceReclaimFixture(t *testing.T, handle func(http.ResponseWriter, *http.Request, string, http.HandlerFunc)) (*Engine, string, func() *Engine) {
	t.Helper()
	worker, id := checkpointFixture(t)
	worker.workspaceHost = "checkpoint-retry-worker"
	worker.controlToken = "checkpoint-retry-test"
	if err := worker.repo.DB.Table("runs").Where("id = ?", id).Updates(map[string]any{
		"workspace_host": worker.workspaceHost,
		"workspace_root": worker.sandboxes.Root(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.checkpointTransfers.Close)
	serveWorkspaceRestoreWorker(t, worker, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		handle(w, r, req.Operation, worker.HandleWorkspaceCheckpoint)
	}))
	return worker, id, func() *Engine {
		e := New(worker.repo, runtime.NewManager(sandboxRoot(t)))
		e.workspaceHost, e.controlToken = "checkpoint-retry-controller", worker.controlToken
		return e
	}
}

func TestConversationWorkspaceReclaimRetryKeepsVerifiedArchive(t *testing.T) {
	t.Parallel()
	var exports, reclaims atomic.Int64
	var dropReply atomic.Bool
	worker, id, controller := remoteWorkspaceReclaimFixture(t, func(w http.ResponseWriter, r *http.Request, operation string, next http.HandlerFunc) {
		switch operation {
		case domain.RunnerWorkspaceOperationCheckpointExport:
			exports.Add(1)
		case domain.RunnerWorkspaceOperationReclaim:
			reclaims.Add(1)
			if dropReply.Load() {
				// The Worker deletes the checkout, but the controller loses
				// the result and must probe the actual directory.
				next(httptest.NewRecorder(), r)
			}
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		next(w, r)
	})
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

func TestConversationWorkspaceRecoveryAfterUnconfirmedReclaim(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name    string
		reclaim bool
	}{
		{name: "checkout removed", reclaim: true},
		{name: "checkout survives"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var probeUnavailable atomic.Bool
			probeUnavailable.Store(true)
			var restores atomic.Int64
			worker, id, controller := remoteWorkspaceReclaimFixture(t, func(w http.ResponseWriter, r *http.Request, operation string, next http.HandlerFunc) {
				switch operation {
				case domain.RunnerWorkspaceOperationReclaim:
					if scenario.reclaim {
						result := httptest.NewRecorder()
						next(result, r)
						if result.Code != http.StatusOK || !bytes.Contains(result.Body.Bytes(), []byte("reclaimed")) {
							t.Errorf("Worker reclaim did not complete: %d %s", result.Code, result.Body)
						}
					}
					http.Error(w, "lost reclaim reply", http.StatusServiceUnavailable)
					return
				case domain.RunnerWorkspaceOperationCheckpointStatus:
					if probeUnavailable.Load() {
						http.Error(w, "status unavailable", http.StatusServiceUnavailable)
						return
					}
				case domain.RunnerWorkspaceOperationRestore:
					restores.Add(1)
				}
				next(w, r)
			})
			e := controller()
			e.cleanupRunWorkspace(id)
			run, _ := worker.repo.RunState(id)
			if run.WorkspaceCleanedAt.IsZero() {
				t.Fatal("unconfirmed reclaim lost its restore marker")
			}
			path := filepath.Join(worker.RunWorkspace(id), "README.md")
			want, wantRestores := "uncommitted", int64(1)
			if !scenario.reclaim {
				want, wantRestores = "newer edit in surviving checkout", 0
				if err := os.WriteFile(path, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e = controller()
			if err := e.RestoreConversationWorkspaceIfNeeded(t.Context(), id); err == nil || restores.Load() != 0 {
				t.Fatalf("restore while status is unavailable: uploads %d, error %v", restores.Load(), err)
			}
			probeUnavailable.Store(false)
			if err := e.RestoreConversationWorkspaceIfNeeded(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != want || restores.Load() != wantRestores {
				t.Fatalf("recovered checkout: got %q, want %q, uploads %d, error %v", content, want, restores.Load(), err)
			}
			run, _ = worker.repo.RunState(id)
			meta, _ := worker.repo.WorkspaceCheckpoint(id)
			if !run.WorkspaceCleanedAt.IsZero() || meta.CleanupRetryAt != nil || meta.LeaseToken != "" {
				t.Fatal("successful restore did not reset the cleanup state")
			}
		})
	}
}

func TestUnconfirmedWorkspaceReclaimProtectsSurvivingCheckoutFromOrphanSweep(t *testing.T) {
	t.Parallel()
	var probeReady, reclaimReady atomic.Bool
	var exports, probes atomic.Int64
	worker, id, controller := remoteWorkspaceReclaimFixture(t, func(w http.ResponseWriter, r *http.Request, operation string, next http.HandlerFunc) {
		switch operation {
		case domain.RunnerWorkspaceOperationCheckpointExport:
			exports.Add(1)
		case domain.RunnerWorkspaceOperationReclaim:
			if !reclaimReady.Load() {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
		case domain.RunnerWorkspaceOperationCheckpointStatus:
			probes.Add(1)
			if !probeReady.Load() {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		next(w, r)
	})
	controller().cleanupRunWorkspace(id)
	run, _ := worker.repo.RunState(id)
	checkpoint, _ := worker.repo.WorkspaceCheckpoint(id)
	if run.WorkspaceCleanedAt.IsZero() || checkpoint.CleanupRetryAt == nil || exports.Load() != 1 {
		t.Fatal("test did not reach unconfirmed cleanup")
	}
	path := filepath.Join(worker.RunWorkspace(id), "README.md")
	const latest = "checkout changed after the last checkpoint"
	if err := os.WriteFile(path, []byte(latest), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.sandboxes.ReclaimExcept(worker.repo.SandboxSweepKeepRunIDs); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != latest {
		t.Fatalf("orphan sweep removed a still unconfirmed checkout: %q, %v", content, err)
	}

	// A replacement controller retries only the root probe and persists the
	// next deadline if the response is still unknown.
	replacement := controller()
	replacement.cleanupWorkspace(run, checkpoint.CleanupRetryAt.Add(-time.Second))
	if probes.Load() != 1 {
		t.Fatal("replacement controller probed before the deadline")
	}
	replacement.cleanupWorkspace(run, *checkpoint.CleanupRetryAt)
	deferred, _ := worker.repo.WorkspaceCheckpoint(id)
	if probes.Load() != 2 || exports.Load() != 1 || deferred.CleanupRetryAt == nil ||
		!deferred.CleanupRetryAt.After(*checkpoint.CleanupRetryAt) {
		t.Fatal("unavailable Worker caused an early probe or another capture")
	}
	probeReady.Store(true)
	replacement.cleanupWorkspace(run, *deferred.CleanupRetryAt)
	run, _ = worker.repo.RunState(id)
	if !run.WorkspaceCleanedAt.IsZero() || exports.Load() != 1 {
		t.Fatal("confirmed surviving checkout was not reopened for recapture")
	}
	reclaimReady.Store(true)
	replacement.cleanupWorkspace(run, *deferred.CleanupRetryAt)
	run, _ = worker.repo.RunState(id)
	confirmed, _ := worker.repo.WorkspaceCheckpoint(id)
	if run.WorkspaceCleanedAt.IsZero() || confirmed.CleanupRetryAt != nil || exports.Load() != 2 || confirmed.ArchiveID == checkpoint.ArchiveID {
		t.Fatal("cleanup did not save the new edit and confirm removal")
	}
	if err := replacement.RestoreConversationWorkspaceIfNeeded(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(path)
	if err != nil || string(content) != latest {
		t.Fatalf("restore lost the last confirmed checkout: %q, %v", content, err)
	}
}

type blockingCheckpointSandbox struct {
	runtime.Sandbox
	closing chan struct{}
	resume  chan struct{}
}

func (s *blockingCheckpointSandbox) Close(context.Context) error {
	close(s.closing)
	<-s.resume
	return nil
}

func TestWorkspaceCheckpointStatusWaitsForInFlightReclaim(t *testing.T) {
	t.Parallel()
	worker, id, controller := remoteWorkspaceReclaimFixture(t, func(w http.ResponseWriter, r *http.Request, _ string, next http.HandlerFunc) {
		next(w, r)
	})
	sandbox := &blockingCheckpointSandbox{closing: make(chan struct{}), resume: make(chan struct{})}
	worker.runEnvs[id] = &runEnvironment{sandbox: sandbox}
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.cleanupRunWorkspace(id)
	}()
	blocked := true
	defer func() {
		if blocked {
			close(sandbox.resume)
		}
		<-done
	}()
	select {
	case <-sandbox.closing:
	case <-done:
		t.Fatal("cleanup exited before closing its workspace")
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not start")
	}
	run, _ := worker.repo.RunState(id)
	status, err := controller().probeRemoteCheckpointRoot(t.Context(), run)
	if err != nil || status.Code != domain.WorkspaceErrorBusy {
		t.Fatalf("Worker reported a usable checkout while reclaim was running: %+v, %v", status, err)
	}
	previous, _ := worker.repo.WorkspaceCheckpoint(id)
	if err := worker.repo.DB.Table("run_workspace_checkpoints").Where("run_id = ?", id).
		Update("lease_until", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	peer := New(worker.repo, runtime.NewManager(worker.sandboxes.Root()))
	peer.workspaceHost = worker.workspaceHost
	token, err := peer.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.repo.FinishWorkspaceUse(id, token, time.Now().UTC(), nil, "") }()
	if err := peer.EnsureConversationWorkspace(t.Context(), id, token); !errors.Is(err, repo.ErrWorkspaceInUse) {
		t.Fatalf("recovery on another engine did not wait for cleanup: %v", err)
	}
	blocked = false
	close(sandbox.resume)
	<-done
	if err := peer.EnsureConversationWorkspace(t.Context(), id, token); err != nil {
		t.Fatal(err)
	}
	if err := worker.reclaimCheckpointWorkspace(id, previous.LeaseToken); !errors.Is(err, repo.ErrWorkspaceInUse) {
		t.Fatalf("finished claim could still reclaim the restored root: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(peer.RunWorkspace(id), "README.md")); err != nil || string(content) != "uncommitted" {
		t.Fatalf("restored file: %q, %v", content, err)
	}
}

func TestWorkspaceRestoreLeaseProtectsCheckoutBeforeBinding(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	e.cleanupRunWorkspace(id)
	run, _ := e.repo.RunState(id)
	checkpoint, _ := e.repo.WorkspaceCheckpoint(id)
	if run.WorkspaceCleanedAt.IsZero() || checkpoint.CleanupRetryAt != nil {
		t.Fatal("initial cleanup did not confirm removal")
	}
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	root := e.RunWorkspace(id)
	if err := e.restoreConversationCheckpoint(t.Context(), run, root); err != nil {
		t.Fatal(err)
	}

	if _, err := e.sandboxes.ReclaimExcept(e.repo.SandboxSweepKeepRunIDs); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.RestoreCheckpointWorkspaceBinding(id, token, e.workspaceHost, e.sandboxes.Root(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(content) != "uncommitted" {
		t.Fatalf("sweep removed the restored files before binding: %q, %v", content, err)
	}
}

func TestOrphanSweepPreservesWorkspaceRestoredAfterOwnerLookup(t *testing.T) {
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			worker, controller, id := remoteWorkspaceRestoreFixture(t, backend)
			serveWorkspaceRestoreWorker(t, worker, http.HandlerFunc(worker.HandleWorkspaceCheckpoint))
			root := worker.RunWorkspace(id)
			// An empty checkout can already be on disk when a restore starts.
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			orphan := worker.RunWorkspace("unregistered-sandbox")
			if err := os.Mkdir(orphan, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "README.md")
			const edited = "new user edits in the restored workspace"
			removed, err := worker.sandboxes.ReclaimExcept(func() ([]string, error) {
				owners, err := worker.repo.SandboxSweepKeepRunIDs()
				if err != nil {
					return nil, err
				}
				token, err := controller.BeginConversationWorkspaceUse(id)
				if err != nil {
					return nil, err
				}
				t.Cleanup(func() { _ = controller.repo.FinishWorkspaceUse(id, token, time.Now().UTC(), nil, "") })
				if err := controller.EnsureConversationWorkspace(t.Context(), id, token); err != nil {
					return nil, err
				}
				if content, err := os.ReadFile(path); err != nil || string(content) != "saved changes" {
					t.Fatalf("restored checkout: %q, %v", content, err)
				}
				return owners, os.WriteFile(path, []byte(edited), 0600)
			})
			if err != nil || removed != 1 {
				t.Fatalf("orphan reclamation removed %d directories: %v", removed, err)
			}
			if content, err := os.ReadFile(path); err != nil || string(content) != edited {
				t.Fatalf("post-restore file: %q, %v", content, err)
			}
			if _, err := os.Stat(orphan); !os.IsNotExist(err) {
				t.Fatalf("orphan remained on disk: %v", err)
			}
		})
	}
}

func TestConversationWorkspaceCleanupRecoversFromFailedCaptureAfterReclaim(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name         string
		removeRoot   bool
		corrupt      bool
		shouldRepair bool
	}{
		{name: "missing root", removeRoot: true},
		{name: "unchanged checkout"},
		{name: "corrupt archive", corrupt: true, shouldRepair: true},
		{name: "missing root and corrupt archive", removeRoot: true, corrupt: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var reclaims atomic.Int64
			var exportUnavailable atomic.Bool
			worker, id, controller := remoteWorkspaceReclaimFixture(t, func(w http.ResponseWriter, r *http.Request, operation string, next http.HandlerFunc) {
				if operation == domain.RunnerWorkspaceOperationReclaim && reclaims.Add(1) == 1 ||
					operation == domain.RunnerWorkspaceOperationCheckpointExport && exportUnavailable.Load() {
					http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
					return
				}
				next(w, r)
			})
			e := controller()
			e.cleanupRunWorkspace(id)
			run, _ := worker.repo.RunState(id)
			first, _ := worker.repo.WorkspaceCheckpoint(id)
			if !run.WorkspaceCleanedAt.IsZero() || first.CleanupRetryAt == nil || first.ArchiveID == "" {
				t.Fatal("surviving checkout did not keep its archive and retry deadline")
			}
			exportUnavailable.Store(true)
			e.cleanupWorkspace(run, *first.CleanupRetryAt)
			failed, _ := worker.repo.WorkspaceCheckpoint(id)
			if failed.Error == "" || failed.SaveRetryAt == nil || failed.ArchiveID != first.ArchiveID {
				t.Fatal("temporary export failure did not preserve the last archive")
			}
			exportUnavailable.Store(false)
			root := worker.RunWorkspace(id)
			if scenario.removeRoot {
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.corrupt {
				if err := worker.repo.DB.Table("run_workspace_checkpoint_chunks").Where("run_id = ? AND archive_id = ?", id, first.ArchiveID).
					Update("data", []byte("corrupt")).Error; err != nil {
					t.Fatal(err)
				}
			}
			e.cleanupWorkspace(run, *failed.SaveRetryAt)
			run, _ = worker.repo.RunState(id)
			current, _ := worker.repo.WorkspaceCheckpoint(id)
			if scenario.removeRoot && scenario.corrupt {
				if !run.WorkspaceCleanedAt.IsZero() || current.Error == "" || reclaims.Load() != 1 {
					t.Fatal("cleanup accepted a corrupt archive with no surviving checkout")
				}
				return
			}
			if run.WorkspaceCleanedAt.IsZero() || current.Error != "" || current.SaveRetryAt != nil ||
				current.SaveFailures != 0 || reclaims.Load() != 2 || (current.ArchiveID != first.ArchiveID) != scenario.shouldRepair {
				t.Fatalf("retry failed to reconcile the archive: %+v", current)
			}
			if err := e.RestoreConversationWorkspaceIfNeeded(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(root, "README.md"))
			if err != nil || string(content) != "uncommitted" {
				t.Fatalf("restore lost saved files: %q, error %v", content, err)
			}
		})
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

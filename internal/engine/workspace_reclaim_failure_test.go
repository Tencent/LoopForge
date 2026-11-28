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

func TestWorkerWorkspaceReclaimAfterFailure(t *testing.T) {
	for _, failure := range []string{"unavailable", "lost reply", "Worker restarts"} {
		t.Run(failure, func(t *testing.T) {
			worker, id := checkpointFixture(t)
			worker.workspaceHost = "reclaim-test-worker"
			worker.controlToken = "reclaim-test"
			if err := worker.repo.DB.Table("runs").Where("id = ?", id).Updates(map[string]any{
				"workspace_host": worker.workspaceHost, "workspace_root": worker.sandboxes.Root(),
			}).Error; err != nil {
				t.Fatal(err)
			}
			var failing atomic.Bool
			failing.Store(true)
			var deleted atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				var req domain.RunnerWorkspaceRequest
				if err := json.Unmarshal(body, &req); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				if failure == "Worker restarts" && deleted.Load() && failing.Load() {
					http.Error(w, "Worker offline", http.StatusServiceUnavailable)
					return
				}
				if req.Operation == domain.RunnerWorkspaceOperationReclaim && failing.Load() {
					if failure != "unavailable" {
						recorded := httptest.NewRecorder()
						worker.HandleWorkspaceCheckpoint(recorded, r)
						if recorded.Code != http.StatusOK {
							t.Errorf("fixture reclaim returned %d", recorded.Code)
						}
						deleted.Store(true)
					}
					http.Error(w, "reclaim response unavailable", http.StatusServiceUnavailable)
					return
				}
				worker.HandleWorkspaceCheckpoint(w, r)
			}))
			defer server.Close()
			if err := worker.repo.HeartbeatEngineInstance(repo.EngineInstance{
				InstanceID: "reclaim-test-worker", Role: "worker", WorkspaceHost: worker.workspaceHost, ControlURL: server.URL,
			}, time.Minute); err != nil {
				t.Fatal(err)
			}

			controller := New(worker.repo, runtime.NewManager(t.TempDir()))
			controller.workspaceHost = "reclaim-test-controller"
			controller.controlToken = worker.controlToken
			controller.reconcileTerminalWorkspaceCleanup()
			first, ok := worker.repo.WorkspaceCheckpoint(id)
			if !ok || first.SavedAt == nil || first.ArchiveID == "" {
				t.Fatalf("missing first checkpoint: %+v", first)
			}
			run, _ := worker.repo.RunState(id)
			checkout := worker.RunWorkspace(id)
			if failure == "unavailable" {
				if !run.WorkspaceCleanedAt.IsZero() {
					t.Fatal("unavailable Worker was marked reclaimed")
				}
				for range 3 {
					controller.reconcileTerminalWorkspaceCleanup()
				}
				checkpoint, _ := worker.repo.WorkspaceCheckpoint(id)
				if checkpoint.ArchiveID != first.ArchiveID {
					t.Fatal("unchanged checkout replaced its saved checkpoint during retry")
				}
				if err := os.WriteFile(filepath.Join(checkout, "late.txt"), []byte("last edit"), 0o600); err != nil {
					t.Fatal(err)
				}
				failing.Store(false)
				controller.reconcileTerminalWorkspaceCleanup()
				run, _ = worker.repo.RunState(id)
			} else if failure == "Worker restarts" {
				if !run.WorkspaceCleanedAt.IsZero() {
					t.Fatal("unconfirmed reclaim was marked finished")
				}
				controller.reconcileTerminalWorkspaceCleanup()
				checkpoint, _ := worker.repo.WorkspaceCheckpoint(id)
				if checkpoint.SaveRetryAt == nil || checkpoint.ArchiveID != first.ArchiveID {
					t.Fatal("offline Worker did not preserve its last checkpoint and record a retry")
				}
				failing.Store(false)
				controller.cleanupWorkspace(run, checkpoint.SaveRetryAt.Add(time.Second))
				run, _ = worker.repo.RunState(id)
				after, _ := worker.repo.WorkspaceCheckpoint(id)
				if after.ArchiveID != first.ArchiveID || after.Error != "" {
					t.Fatalf("Worker restarted with an unusable checkpoint: %+v", after)
				}
			}
			if run.WorkspaceCleanedAt.IsZero() {
				t.Fatal("the missing checkout was not recognized as reclaimed")
			}
			if _, err := os.Lstat(checkout); !os.IsNotExist(err) {
				t.Fatalf("the Worker still has an active checkout: %v", err)
			}
			token, err := controller.BeginConversationWorkspaceUse(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := controller.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
				t.Fatal(err)
			}
			if err := controller.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
				t.Fatal(err)
			}
			if body, err := os.ReadFile(filepath.Join(checkout, "README.md")); err != nil || string(body) != "uncommitted" {
				t.Fatalf("saved file: %q, %v", body, err)
			}
			if failure == "unavailable" {
				if body, err := os.ReadFile(filepath.Join(checkout, "late.txt")); err != nil || string(body) != "last edit" {
					t.Fatalf("late edit: %q, %v", body, err)
				}
			}
		})
	}
}

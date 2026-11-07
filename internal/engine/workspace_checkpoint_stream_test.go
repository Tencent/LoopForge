package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type checkpointCountingWriter struct {
	http.ResponseWriter
	size int64
}

func TestConversationWorkspaceLegacyCheckpointFallback(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run, _ := e.repo.RunState(id)
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
	run.ExecutionContext.ExecutionRunnerID = "legacy-runner"
	calls := 0
	archive := runtime.EmptyWorkspaceCheckpoint()
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		calls++
		if req.Operation == domain.RunnerWorkspaceOperationCheckpointExport {
			return domain.RunnerWorkspaceResponse{Status: 400, Error: "unsupported operation"}, nil
		}
		if req.Operation != domain.RunnerWorkspaceOperationCheckpoint {
			t.Fatal("unexpected legacy fallback", req.Operation)
		}
		payload, _ := json.Marshal(map[string][]byte{"checkpoint": archive})
		return domain.RunnerWorkspaceResponse{Payload: payload}, nil
	}
	var restored bytes.Buffer
	if err := e.downloadConversationCheckpoint(context.Background(), run, &restored); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !bytes.Equal(restored.Bytes(), archive) {
		t.Fatal("small legacy runner compatibility lost", calls)
	}
}

func TestConversationWorkspaceSurvivingDirectoryRequiresSavedChecksum(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	e.reconcileTerminalWorkspaceCleanup()
	root := e.RunWorkspace(id)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("newer surviving files"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", id).Update("sha256", "invalid").Error; err != nil {
		t.Fatal(err)
	}
	if err := e.RestoreConversationWorkspaceIfNeeded(context.Background(), id); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatal("surviving directory bypassed corrupt snapshot", err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(content) != "newer surviving files" {
		t.Fatal("existing files overwritten")
	}
}

func (w *checkpointCountingWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.size += int64(n)
	return n, err
}

func TestConversationWorkspaceChunkedWorkerTransportRoundTrip(t *testing.T) {
	t.Parallel()
	worker, id := checkpointFixture(t)
	defer worker.checkpointTransfers.Close()
	worker.workspaceHost = "checkpoint-worker"
	worker.controlToken = "shared-test-token"
	if err := worker.repo.DB.Table("runs").Where("id = ?", id).Updates(map[string]any{"workspace_host": worker.workspaceHost, "workspace_root": worker.sandboxes.Root()}).Error; err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 21<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worker.RunWorkspace(id), "large.bin"), data, 0755); err != nil {
		t.Fatal(err)
	}
	var maximum atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 8<<20 {
			t.Errorf("oversized request: %d", r.ContentLength)
		}
		count := &checkpointCountingWriter{ResponseWriter: w}
		worker.HandleWorkspaceCheckpoint(count, r)
		for old := maximum.Load(); old < count.size; old = maximum.Load() {
			if maximum.CompareAndSwap(old, count.size) {
				break
			}
		}
	}))
	defer server.Close()
	if err := worker.repo.HeartbeatEngineInstance(repo.EngineInstance{InstanceID: worker.workspaceHost, Role: "worker", WorkspaceHost: worker.workspaceHost, ControlURL: server.URL}, time.Minute); err != nil {
		t.Fatal(err)
	}
	controller := New(worker.repo, runtime.NewManager(t.TempDir()))
	controller.workspaceHost = "checkpoint-controller"
	controller.controlToken = worker.controlToken
	controller.reconcileTerminalWorkspaceCleanup()
	run, _ := worker.repo.RunState(id)
	meta, _ := worker.repo.WorkspaceCheckpoint(id)
	if run.WorkspaceCleanedAt.IsZero() || meta.ArchiveID == "" || meta.ArchiveBytes <= 16<<20 {
		t.Fatalf("large archive not persisted/reclaimed: %+v", meta)
	}
	if _, err := os.Stat(worker.RunWorkspace(id)); !os.IsNotExist(err) {
		t.Fatal("source not reclaimed", err)
	}
	token, err := controller.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(worker.RunWorkspace(id), "large.bin"))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("large remote file lost", err)
	}
	if err = controller.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	if maximum.Load() > 8<<20 {
		t.Fatal("response exceeded bounded chunk transport", maximum.Load())
	}
}

func TestConversationWorkspaceMaintenanceDoesNotLeaseRecentlyUsedFiles(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	token, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.EnsureConversationWorkspace(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	if err = e.EndConversationWorkspaceUse(context.Background(), id, token); err != nil {
		t.Fatal(err)
	}
	// A terminal or IDE read recently touched this workspace. Maintenance must
	// return without even acquiring the exclusive lease or attempting capture.
	if err = e.repo.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", id).Update("error", "previous save failure").Error; err != nil {
		t.Fatal(err)
	}
	root := e.RunWorkspace(id)
	if err = os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e.reconcileTerminalWorkspaceCleanup()
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if meta.LeaseToken != "" || meta.Error != "previous save failure" {
		t.Fatal("maintenance competed with recent user activity")
	}
	// The saved archive can still be read even though a later capture would fail.
	source, err := e.repo.OpenWorkspaceCheckpointArchive(id)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err = io.Copy(io.Discard, source); err != nil {
		t.Fatal(err)
	}
}

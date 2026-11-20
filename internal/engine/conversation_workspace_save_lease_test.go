package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// A background save inherits the lease the foreground turn handed over
// (HandoffWorkspaceUseToSave keeps lease_until). Building and uploading an
// archive can outlive that lease, and the publish steps only accept a live
// lease, so the same token can never win: every retry re-packages the whole
// workspace and fails the same way. The save must take its own lease instead
// of discarding the files it already packaged.
func TestBackgroundSavePublishesAfterItsLeaseLapses(t *testing.T) {
	e, id := checkpointFixture(t)
	if err := os.WriteFile(filepath.Join(e.RunWorkspace(id), "notes.md"), []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	use, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	saveToken, err := e.repo.HandoffWorkspaceUseToSave(id, use, now)
	if err != nil {
		t.Fatal(err)
	}
	// The archive work outlives the handed-over lease.
	if err = e.repo.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", id).Update("lease_until", now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	run, _ := e.repo.RunState(id)
	if err = e.saveConversationWorkspace(context.Background(), run, saveToken); err != nil {
		t.Fatalf("save with a lapsed own lease: %v", err)
	}
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if meta.SavedAt == nil || meta.Error != "" {
		t.Fatalf("save did not publish: %+v", meta)
	}
	archive, err := e.repo.WorkspaceCheckpointArchive(id)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "restored")
	if err = runtime.RestoreWorkspaceCheckpoint(root, archive); err != nil {
		t.Fatal(err)
	}
	if content, readErr := os.ReadFile(filepath.Join(root, "notes.md")); readErr != nil || string(content) != "keep me" {
		t.Fatalf("published archive lost files: %q %v", content, readErr)
	}
}

// remoteCheckpointTransport serves the real chunked checkpoint transfer so the
// tests exercise the production save path: export, read, commit, close.
type remoteCheckpointTransport struct {
	archive []byte
	busy    func(call int64) bool
	calls   atomic.Int64
	exports atomic.Int64
}

func (r *remoteCheckpointTransport) serve(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
	call := r.calls.Add(1)
	switch request.Operation {
	case domain.RunnerWorkspaceOperationCheckpointExport:
		r.exports.Add(1)
		if r.busy != nil && r.busy(call) {
			return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: "workspace is still in use"}, nil
		}
		sum := sha256.Sum256(r.archive)
		payload, err := json.Marshal(domain.WorkspaceCheckpointTransfer{ID: "transfer-1", Size: int64(len(r.archive)), SHA256: hex.EncodeToString(sum[:])})
		if err != nil {
			return domain.RunnerWorkspaceResponse{}, err
		}
		return domain.RunnerWorkspaceResponse{Status: 200, Payload: payload}, nil
	case domain.RunnerWorkspaceOperationCheckpointRead:
		offset := int(request.CheckpointTransfer.Offset)
		end := min(offset+runtime.WorkspaceCheckpointChunkBytes, len(r.archive))
		payload, err := json.Marshal(domain.WorkspaceCheckpointTransfer{ID: "transfer-1", Offset: int64(offset), Data: r.archive[offset:end]})
		if err != nil {
			return domain.RunnerWorkspaceResponse{}, err
		}
		return domain.RunnerWorkspaceResponse{Status: 200, Payload: payload}, nil
	case domain.RunnerWorkspaceOperationCheckpointClose:
		return domain.RunnerWorkspaceResponse{Status: 200}, nil
	default:
		return domain.RunnerWorkspaceResponse{Status: 200}, nil
	}
}

func remoteConversationFixture(t *testing.T) (*Engine, string) {
	t.Helper()
	e, id := checkpointFixture(t)
	run, _ := e.repo.RunState(id)
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
	run.ExecutionContext.ExecutionRunnerID = "test-remote"
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return e, id
}

// A runner gate that is still releasing is transient: the save must still
// publish, so tightening the retry budget cannot turn a recoverable busy into
// a dropped snapshot.
func TestBackgroundSavePublishesAfterTransientRunnerBusy(t *testing.T) {
	e, id := remoteConversationFixture(t)
	archive, err := runtime.CreateWorkspaceCheckpoint(e.RunWorkspace(id))
	if err != nil {
		t.Fatal(err)
	}
	transport := &remoteCheckpointTransport{archive: archive, busy: func(call int64) bool { return call <= 2 }}
	e.WorkspaceCheckpointTransport = transport.serve
	use, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	done, err := e.EndConversationWorkspaceUseAsync(context.Background(), id, use)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("transient runner busy failed the save: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("transient runner busy never recovered (%d exports)", transport.exports.Load())
	}
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if meta.SavedAt == nil || meta.Error != "" {
		t.Fatalf("save did not publish after a transient busy: %+v", meta)
	}
}

// A runner gate that stays busy must not make the whole turn re-package and
// re-upload the workspace ten times a second for five minutes. The save stops,
// records the failure and lets the existing retry backoff own the next attempt.
func TestBackgroundSaveStopsRetryingWhileRunnerStaysBusy(t *testing.T) {
	e, id := remoteConversationFixture(t)
	archive, err := runtime.CreateWorkspaceCheckpoint(e.RunWorkspace(id))
	if err != nil {
		t.Fatal(err)
	}
	transport := &remoteCheckpointTransport{archive: archive, busy: func(int64) bool { return true }}
	e.WorkspaceCheckpointTransport = transport.serve
	use, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	done, err := e.EndConversationWorkspaceUseAsync(context.Background(), id, use)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("busy save reported success")
		}
	case <-time.After(6 * time.Second):
		t.Fatalf("save kept retrying a persistent busy gate (%d exports)", transport.exports.Load())
	}
	if n := transport.exports.Load(); n > 40 {
		t.Fatalf("save retried %d times before giving up", n)
	}
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if meta.Error == "" || meta.SaveRetryAt == nil || meta.LeaseToken != "" {
		t.Fatalf("persistent busy was not recorded for backoff: %+v", meta)
	}
	if _, err = os.Stat(filepath.Join(e.RunWorkspace(id), "README.md")); err != nil {
		t.Fatal("failed save lost workspace files", err)
	}
}

// The chunked archive path writes every 4 MiB chunk before the manifest CAS,
// so a lease that lapses during the upload leaves the whole archive written and
// discarded on each retry. The save must publish it instead, and leave no
// staging rows behind.
func TestBackgroundSaveRepublishesChunkedArchiveAfterLapsedLease(t *testing.T) {
	e, id := checkpointFixture(t)
	big := make([]byte, 13<<20)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.RunWorkspace(id), "big.bin"), big, 0600); err != nil {
		t.Fatal(err)
	}
	use, err := e.BeginConversationWorkspaceUse(id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	saveToken, err := e.repo.HandoffWorkspaceUseToSave(id, use, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.repo.DB.Model(&repo.WorkspaceCheckpoint{}).Where("run_id = ?", id).Update("lease_until", now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	run, _ := e.repo.RunState(id)
	if err = e.saveConversationWorkspace(context.Background(), run, saveToken); err != nil {
		t.Fatalf("chunked save with a lapsed own lease: %v", err)
	}
	meta, _ := e.repo.WorkspaceCheckpoint(id)
	if meta.SavedAt == nil || meta.ArchiveID == "" || meta.ArchiveBytes <= 12<<20 {
		t.Fatalf("chunked save did not publish: %+v", meta)
	}
	var staging int64
	if err = e.repo.DB.Raw("SELECT COUNT(*) FROM run_workspace_checkpoint_chunks WHERE run_id = ? AND archive_id <> ?", id, meta.ArchiveID).Scan(&staging).Error; err != nil {
		t.Fatal(err)
	}
	if staging != 0 {
		t.Fatalf("%d staging chunks survived the save", staging)
	}
	archive, err := e.repo.WorkspaceCheckpointArchive(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.ValidateWorkspaceCheckpointFrom(bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "restored")
	if err = runtime.RestoreWorkspaceCheckpointFrom(root, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Stat(filepath.Join(root, "big.bin")); statErr != nil || info.Size() != int64(len(big)) {
		t.Fatalf("published chunked archive lost files: %v %v", info, statErr)
	}
}

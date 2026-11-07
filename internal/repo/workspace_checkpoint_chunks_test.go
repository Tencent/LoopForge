package repo

import (
	"agentflow/internal/domain"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

func checkpointChunkTest(t *testing.T, s *Store) {
	t.Helper()
	run := domain.Run{ID: fmt.Sprintf("checkpoint-chunks-%d", time.Now().UnixNano()), Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DeleteRun(run.ID) })
	now := time.Now().UTC().Truncate(time.Millisecond)
	token, err := s.BeginWorkspaceUse(run.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	// Larger than one MEDIUMBLOB and the legacy RPC; every row stays <=4 MiB.
	data := bytes.Repeat([]byte("checkpoint-content"), (20<<20)/18+1)
	if err = s.SaveWorkspaceCheckpointStream(run.ID, token, now, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	meta, _ := s.WorkspaceCheckpoint(run.ID)
	if meta.ArchiveID == "" || meta.ArchiveBytes != int64(len(data)) || meta.ArchiveChunks < 5 {
		t.Fatalf("manifest %+v", meta)
	}
	var largeRows int64
	if err = s.DB.Model(&workspaceCheckpointChunk{}).Where("run_id = ? AND LENGTH(data) > ?", run.ID, workspaceCheckpointChunkBytes).Count(&largeRows).Error; err != nil || largeRows != 0 {
		t.Fatal(largeRows, err)
	}
	read := func() ([]byte, error) {
		r, err := s.OpenWorkspaceCheckpointArchive(run.ID)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}
	actual, err := read()
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("chunk round trip failed", err)
	}
	if err = s.SaveWorkspaceCheckpointStream(run.ID, token, now, &checkpointFailingReader{remaining: workspaceCheckpointInlineBytes + workspaceCheckpointChunkBytes + 100}); err == nil {
		t.Fatal("partial save succeeded")
	}
	current, _ := s.WorkspaceCheckpoint(run.ID)
	if current.ArchiveID != meta.ArchiveID {
		t.Fatal("failed save replaced previous manifest")
	}
	actual, err = read()
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("failed save corrupted previous archive", err)
	}
	if err = s.SaveWorkspaceCheckpointStream(run.ID, "stale-token", now, bytes.NewReader(data)); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatal("stale writer committed", err)
	}
	if err = s.DB.Model(&workspaceCheckpointChunk{}).Where("run_id = ? AND sequence = ?", run.ID, 1).Update("data", bytes.Repeat([]byte("x"), workspaceCheckpointChunkBytes)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = read(); err == nil {
		t.Fatal("corrupt chunk accepted")
	}
	if err = s.SaveWorkspaceCheckpointStream(run.ID, token, now, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	var count int64
	s.DB.Model(&workspaceCheckpointChunk{}).Where("run_id = ?", run.ID).Count(&count)
	if count != int64(meta.ArchiveChunks) {
		t.Fatal("staging or obsolete chunks leaked", count)
	}
	if err = s.FinishWorkspaceUse(run.ID, token, now, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
	s.DB.Model(&workspaceCheckpointChunk{}).Where("run_id = ?", run.ID).Count(&count)
	if count != 0 {
		t.Fatal("deleted run retained chunks", count)
	}
}

type checkpointFailingReader struct{ remaining int }

func (r *checkpointFailingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, errors.New("interrupted transfer")
	}
	n := min(len(p), r.remaining)
	clear(p[:n])
	r.remaining -= n
	return n, nil
}
func TestWorkspaceCheckpointChunksAtomicRoundTrip(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkpointChunkTest(t, s)
}
func TestMySQLWorkspaceCheckpointChunks(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated Docker MySQL required")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkpointChunkTest(t, s)
}

func TestWorkspaceCheckpointSharedBackoff(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkpointBackoffTest(t, s, s.RunsRepo)
}
func checkpointBackoffTest(t *testing.T, s *Store, other *RunsRepo) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	idle := now.Add(-24 * time.Hour)
	run := domain.Run{ID: fmt.Sprintf("checkpoint-backoff-%d", time.Now().UnixNano()), Status: "succeeded", UpdatedAt: idle, Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	defer s.DeleteRun(run.ID)
	if err := s.DB.Table("runs").Where("id = ?", run.ID).Update("updated_at", idle).Error; err != nil {
		t.Fatal(err)
	}
	token, err := s.BeginWorkspaceCheckpointSave(run.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorkspaceCheckpointSave(run.ID, token, idle, now, "archive too large"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err = other.BeginWorkspaceCheckpointSave(run.ID, now.Add(time.Duration(i)*20*time.Second)); !errors.Is(err, ErrWorkspaceCheckpointBackoff) {
			t.Fatalf("instance retried early: %v", err)
		}
	}
	meta, _ := s.WorkspaceCheckpoint(run.ID)
	if !meta.LastUsedAt.Equal(idle) || meta.SaveRetryAt == nil || !meta.SaveRetryAt.After(now) {
		t.Fatalf("backoff uses idle timestamp: %+v", meta)
	}
	// A real foreground turn is always allowed and can repair the workspace.
	token, err = other.BeginWorkspaceUse(run.ID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorkspaceUse(run.ID, token, now.Add(time.Second), nil, ""); err != nil {
		t.Fatal(err)
	}
	meta, _ = s.WorkspaceCheckpoint(run.ID)
	if meta.Error == "" || meta.SaveFailures != 1 {
		t.Fatal("no-op use cleared save backoff")
	}
	token, err = s.BeginWorkspaceUse(run.ID, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveWorkspaceCheckpointStream(run.ID, token, now.Add(2*time.Second), bytes.NewReader([]byte("repaired"))); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishWorkspaceUse(run.ID, token, now.Add(2*time.Second), nil, ""); err != nil {
		t.Fatal(err)
	}
	meta, _ = s.WorkspaceCheckpoint(run.ID)
	if meta.Error != "" || meta.SaveRetryAt != nil || meta.SaveFailures != 0 || meta.ArchiveID != "" {
		t.Fatal("successful save did not reset backoff")
	}
}
func TestMySQLWorkspaceCheckpointSharedBackoff(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated Docker MySQL required")
	}
	a, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	checkpointBackoffTest(t, a, b.RunsRepo)
}

func TestWorkspaceCheckpointHeartbeatDuringCaptureDefersCleanup(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)
	idle := now.Add(-2 * time.Hour)
	run := domain.Run{ID: "heartbeat-during-capture", Status: "succeeded", UpdatedAt: idle, Nodes: map[string]*domain.NodeRun{}}
	if err = s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	run, _ = s.RunState(run.ID)
	if err = s.DB.Table("runs").Where("id = ?", run.ID).Update("updated_at", idle).Error; err != nil {
		t.Fatal(err)
	}
	token, err := s.BeginWorkspaceCheckpointSave(run.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveWorkspaceCheckpointStream(run.ID, token, now, bytes.NewReader([]byte("snapshot"))); err != nil {
		t.Fatal(err)
	}
	touched := now.Add(time.Second)
	if err = s.TouchWorkspaceUse(run.ID, touched); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimCheckpointWorkspaceCleanup(run.ID, token, run.Revision, now.Add(2*time.Second)); err != nil || claimed {
		t.Fatal("active reader reclaimed during capture", claimed, err)
	}
	if err = s.FinishWorkspaceCheckpointSave(run.ID, token, idle, now.Add(2*time.Second), ""); err != nil {
		t.Fatal(err)
	}
	meta, _ := s.WorkspaceCheckpoint(run.ID)
	if !meta.LastUsedAt.Equal(touched) {
		t.Fatal("background release overwrote new heartbeat", meta.LastUsedAt)
	}
}

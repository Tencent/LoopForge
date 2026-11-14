package repo

import (
	"agentflow/internal/domain"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestStageWorkspaceCheckpointChunks(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := domain.Run{ID: fmt.Sprintf("checkpoint-stage-%d", time.Now().UnixNano()), Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	token, err := s.BeginWorkspaceUse(run.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	chunksOf := func(archiveID string) []workspaceCheckpointChunk {
		var rows []workspaceCheckpointChunk
		if err := s.DB.Where("run_id = ? AND archive_id = ?", run.ID, archiveID).Order("sequence").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		return rows
	}

	t.Run("multi chunk", func(t *testing.T) {
		data := bytes.Repeat([]byte("stage"), (2*workspaceCheckpointChunkBytes+100)/5)
		size, count, sum, err := s.stageWorkspaceCheckpointChunks(run.ID, token, "archive-multi", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		want := sha256.Sum256(data)
		if size != int64(len(data)) || count != 3 || sum != hex.EncodeToString(want[:]) {
			t.Fatalf("size=%d count=%d sum=%s", size, count, sum)
		}
		rows := chunksOf("archive-multi")
		if len(rows) != 3 {
			t.Fatalf("rows=%d", len(rows))
		}
		var joined []byte
		for i, row := range rows {
			if row.Sequence != i || len(row.Data) > workspaceCheckpointChunkBytes {
				t.Fatalf("row %d: sequence=%d len=%d", i, row.Sequence, len(row.Data))
			}
			joined = append(joined, row.Data...)
		}
		if !bytes.Equal(joined, data) {
			t.Fatal("staged rows do not reassemble source")
		}
	})

	t.Run("exact chunk boundary", func(t *testing.T) {
		data := bytes.Repeat([]byte("x"), workspaceCheckpointChunkBytes)
		size, count, _, err := s.stageWorkspaceCheckpointChunks(run.ID, token, "archive-exact", bytes.NewReader(data))
		if err != nil || size != int64(len(data)) || count != 1 || len(chunksOf("archive-exact")) != 1 {
			t.Fatalf("size=%d count=%d err=%v", size, count, err)
		}
	})

	t.Run("empty source", func(t *testing.T) {
		size, count, _, err := s.stageWorkspaceCheckpointChunks(run.ID, token, "archive-empty", bytes.NewReader(nil))
		if err != nil || size != 0 || count != 0 || len(chunksOf("archive-empty")) != 0 {
			t.Fatalf("size=%d count=%d err=%v", size, count, err)
		}
	})

	t.Run("stale lease", func(t *testing.T) {
		_, _, _, err := s.stageWorkspaceCheckpointChunks(run.ID, "stale-token", "archive-stale", bytes.NewReader([]byte("data")))
		if !errors.Is(err, ErrWorkspaceInUse) {
			t.Fatalf("err=%v", err)
		}
		if rows := chunksOf("archive-stale"); len(rows) != 0 {
			t.Fatalf("stale writer staged %d rows", len(rows))
		}
	})

	t.Run("read error", func(t *testing.T) {
		_, _, _, err := s.stageWorkspaceCheckpointChunks(run.ID, token, "archive-fail", &checkpointFailingReader{remaining: workspaceCheckpointChunkBytes + 10})
		if err == nil {
			t.Fatal("read failure ignored")
		}
	})
}

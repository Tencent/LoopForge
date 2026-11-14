package repo

import (
	"agentflow/internal/domain"
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"
)

// leaseLosingReader serves data and runs lose once the first full chunk has
// been handed out, i.e. while the stager is about to write the second row.
type leaseLosingReader struct {
	data   *bytes.Reader
	served int
	lose   func()
	lost   bool
}

func (r *leaseLosingReader) Read(p []byte) (int, error) {
	if !r.lost && r.served >= workspaceCheckpointChunkBytes {
		r.lost = true
		r.lose()
	}
	n, err := r.data.Read(p)
	r.served += n
	return n, err
}

// stageWorkspaceCheckpointChunks documents that it re-checks the lease before
// every row. These guard that a writer whose lease is lost mid-stream stops
// at the next row instead of only checking before the first one.
func TestStageWorkspaceCheckpointChunksStopsWhenLeaseLostMidStream(t *testing.T) {
	cases := []struct {
		name string
		lose func(s *Store, runID string) error
	}{
		{"lease expired", func(s *Store, runID string) error {
			return s.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ?", runID).Update("lease_until", time.Now().UTC().Add(-time.Minute)).Error
		}},
		{"lease taken over", func(s *Store, runID string) error {
			return s.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ?", runID).Update("lease_token", "other-writer").Error
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			run := domain.Run{ID: fmt.Sprintf("checkpoint-stage-lease-%d", time.Now().UnixNano()), Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}
			token, err := s.BeginWorkspaceUse(run.ID, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			source := &leaseLosingReader{
				data: bytes.NewReader(bytes.Repeat([]byte("l"), 3*workspaceCheckpointChunkBytes)),
				lose: func() {
					if err := tc.lose(s, run.ID); err != nil {
						t.Fatal(err)
					}
				},
			}
			_, _, _, err = s.stageWorkspaceCheckpointChunks(run.ID, token, "archive-lease-lost", source)
			if !errors.Is(err, ErrWorkspaceInUse) {
				t.Fatalf("writer kept staging after losing its lease: err=%v", err)
			}
			if !source.lost {
				t.Fatal("lease was never lost; the test did not reach the second chunk")
			}
			var rows []workspaceCheckpointChunk
			if err := s.DB.Where("run_id = ? AND archive_id = ?", run.ID, "archive-lease-lost").Order("sequence").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Sequence != 0 {
				t.Fatalf("only the row staged before the lease was lost may exist, got %d rows", len(rows))
			}
		})
	}
}

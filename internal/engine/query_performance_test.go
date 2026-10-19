package engine

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"gorm.io/gorm"
)

func TestSubscriptionSkipsUnchangedRunPayloads(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutRun(domain.Run{ID: "poll-query", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	var childReads atomic.Int64
	if err := s.DB.Callback().Query().After("gorm:query").Register("count_poll_children", func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case "run_nodes", "run_artifacts", "run_logs", "run_telemetry":
			childReads.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	updates, cancel, err := e.Subscribe("poll-query")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	initial := <-updates
	before := childReads.Load()
	select {
	case <-updates:
		t.Fatal("unchanged revision was republished")
	case <-time.After(750 * time.Millisecond):
	}
	if childReads.Load() != before {
		t.Fatal("idle subscription reread run payloads")
	}
	if err := s.DB.Exec("UPDATE runs SET revision=revision+1, status='failed' WHERE id=?", "poll-query").Error; err != nil {
		t.Fatal(err)
	}
	select {
	case next := <-updates:
		if next.Revision != initial.Revision+1 || next.Status != "failed" {
			t.Fatal("external update was not observed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not observe revision change")
	}
}

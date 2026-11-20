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
	t.Parallel()
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

func TestRunPublicationKeepsLiveLogAndTelemetryDeltas(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const id = "publish-stream-deltas"
	run := domain.Run{ID: id, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	updates, cancel, err := e.Subscribe(id)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	initial := <-updates

	run.Revision = initial.Revision
	run.LogSequence = 1
	run.TelemetrySequence = 1
	run.Logs = []domain.LogRecord{{Sequence: 1, Body: "approval requested"}}
	run.Telemetry = []domain.TelemetryEvent{{Sequence: 1, Name: "approval.waiting"}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.publish(id)
	select {
	case next := <-updates:
		if next.Revision != initial.Revision+1 || next.LogSequence != 1 || next.TelemetrySequence != 1 {
			t.Fatalf("unexpected revision/cursor values: revision=%d log=%d telemetry=%d", next.Revision, next.LogSequence, next.TelemetrySequence)
		}
		if len(next.Logs) != 1 || next.Logs[0].Body != "approval requested" {
			t.Fatalf("logs at cursor 1 = %+v, want the record that advanced the cursor", next.Logs)
		}
		if len(next.Telemetry) != 1 || next.Telemetry[0].Name != "approval.waiting" {
			t.Fatalf("telemetry at cursor 1 = %+v, want the event that advanced the cursor", next.Telemetry)
		}
	default:
		t.Fatal("explicit publication did not produce a snapshot")
	}
}

package runeval

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestElapsedMs(t *testing.T) {
	start := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	if got := elapsedMs(time.Time{}, start); got != 0 {
		t.Fatalf("zero start: got %d, want 0", got)
	}
	if got := elapsedMs(start, start.Add(1500*time.Millisecond)); got != 1500 {
		t.Fatalf("closed span: got %d, want 1500", got)
	}
	if got := elapsedMs(start, start.Add(-time.Minute)); got != 0 {
		t.Fatalf("negative span: got %d, want 0", got)
	}
	open := time.Now().UTC().Add(-2 * time.Second)
	if got := elapsedMs(open, time.Time{}); got < 2000 {
		t.Fatalf("open span: got %d, want >= 2000", got)
	}
	if got := elapsedMs(time.Now().UTC().Add(time.Hour), time.Time{}); got != 0 {
		t.Fatalf("future start with open end: got %d, want 0", got)
	}
}

func TestOrderedNodeIDsKeepsSnapshotOrderThenRuntimeExtras(t *testing.T) {
	run := domain.Run{
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "b"}, {ID: "a"}}},
		Nodes: map[string]*domain.NodeRun{
			"a":     {NodeID: "a"},
			"extra": {NodeID: "extra"},
		},
	}
	got := orderedNodeIDs(run)
	want := []string{"b", "a", "extra"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if ids := orderedNodeIDs(domain.Run{}); len(ids) != 0 {
		t.Fatalf("empty run: got %v", ids)
	}
}

func TestBuildFactsDurationsUseElapsedMs(t *testing.T) {
	start := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	run := domain.Run{
		CreatedAt:        start,
		UpdatedAt:        start.Add(-time.Second),
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "a"}, {ID: "b"}, {ID: "c"}}},
		Nodes: map[string]*domain.NodeRun{
			"a": {StartedAt: start, FinishedAt: start.Add(3 * time.Second)},
			"b": {StartedAt: start, FinishedAt: start.Add(2 * time.Second)},
			"c": {},
		},
	}
	facts := BuildFacts(run, domain.RunEvalCatalog{})
	if facts.WallDurationMs != 0 {
		t.Fatalf("wall duration should clamp to 0, got %d", facts.WallDurationMs)
	}
	if facts.ExecDurationMs != 5000 {
		t.Fatalf("exec duration: got %d, want 5000", facts.ExecDurationMs)
	}
	if len(facts.Nodes) != 3 || facts.Nodes[0].DurationMs != 3000 || facts.Nodes[2].DurationMs != 0 {
		t.Fatalf("unexpected node facts: %+v", facts.Nodes)
	}
}

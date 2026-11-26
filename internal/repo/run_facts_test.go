package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestQueryRunFactsResumesAfterPersistedCursor(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	run := domain.Run{ID: "fact-run", Status: "failed", Events: []domain.RunEvent{
		{ID: "started", Type: "run.started"},
		{ID: "approval", NodeID: "review", Type: "gate.waiting"},
		{ID: "decision", NodeID: "review", Type: "gate.approved"},
		{ID: "finished", Type: "run.failed"},
	}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	first, err := store.QueryRunFacts(run.ID, 0, 1)
	if err != nil || !first.HasMore || len(first.Events) != 1 || first.Events[0].EventID != "approval" {
		t.Fatalf("first page = %+v, err = %v", first, err)
	}
	second, err := store.QueryRunFacts(run.ID, first.NextAfter, 10)
	if err != nil || second.HasMore || len(second.Events) != 2 || second.Events[0].EventID != "decision" || second.Events[1].EventID != "finished" {
		t.Fatalf("second page = %+v, err = %v", second, err)
	}
	repeated, err := store.QueryRunFacts(run.ID, 0, 1)
	if err != nil || len(repeated.Events) != 1 || repeated.Events[0] != first.Events[0] {
		t.Fatalf("repeated page = %+v, err = %v", repeated, err)
	}
}

package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestRunRevisionAdvancesAfterConcurrentControlWrite(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	const id = "revision-after-pause"
	if err := s.PutRun(domain.Run{ID: id, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	// The engine reads the run while the API can independently request pause.
	stale, ok := s.RunState(id)
	if !ok {
		t.Fatal("run missing before pause")
	}
	paused, err := s.RequestRunPause(id)
	if err != nil {
		t.Fatal(err)
	}
	stale.Input = "updated by the engine"
	if err := s.PutRun(stale); err != nil {
		t.Fatal(err)
	}
	got, ok := s.RunState(id)
	if !ok || got.Input != stale.Input || !got.PauseRequested {
		t.Fatalf("engine write lost state or pause request: %+v", got)
	}
	if got.Revision != paused.Revision+1 {
		t.Fatalf("engine write reused revision %d after pause at revision %d", got.Revision, paused.Revision)
	}
}

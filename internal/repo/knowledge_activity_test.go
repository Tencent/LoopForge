package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestKnowledgeActivityIsPersistedAndProjectScoped(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	base := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := s.PutKnowledgeActivity(domain.KnowledgeActivity{
			At:        base.Add(time.Duration(i) * time.Second),
			Tool:      "kb_search",
			ProjectID: "p1",
			AgentID:   "agent-1",
			UserID:    7,
			RunID:     "run-1",
			SessionID: "chat-1",
			Query:     fmt.Sprintf("q%d", i),
			Hits:      i,
			OK:        true,
			LatencyMS: 12,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutKnowledgeActivity(domain.KnowledgeActivity{
		At: base, Tool: "kb_fetch", ProjectID: "p2", Query: "x.md", OK: false, Error: "不在授权范围",
	}); err != nil {
		t.Fatal(err)
	}

	got := s.KnowledgeActivities("p1", 10)
	if len(got) != 3 {
		t.Fatalf("p1 activity count = %d, want 3: %#v", len(got), got)
	}
	first := got[0]
	if first.Query != "q2" || first.Tool != "kb_search" || first.AgentID != "agent-1" || first.UserID != 7 ||
		first.RunID != "run-1" || first.SessionID != "chat-1" || !first.OK || first.Hits != 2 || first.LatencyMS != 12 {
		t.Fatalf("activity fields not preserved: %#v", first)
	}
	if !first.At.Equal(base.Add(2 * time.Second)) {
		t.Fatalf("activity time = %s", first.At)
	}
	if other := s.KnowledgeActivities("p2", 10); len(other) != 1 || other[0].OK || other[0].Error == "" {
		t.Fatalf("p2 activity = %#v", other)
	}
	if empty := s.KnowledgeActivities("", 10); len(empty) != 0 {
		t.Fatalf("activity without a project must stay hidden: %#v", empty)
	}
}

func TestKnowledgeActivityCapPerProject(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	limit := knowledgeActivityRetention
	for i := 0; i < limit+5; i++ {
		if err := s.PutKnowledgeActivity(domain.KnowledgeActivity{
			At: time.Now().UTC(), Tool: "kb_search", ProjectID: "p1", Query: fmt.Sprintf("q%d", i), OK: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutKnowledgeActivity(domain.KnowledgeActivity{At: time.Now().UTC(), Tool: "kb_search", ProjectID: "p2", Query: "keep"}); err != nil {
		t.Fatal(err)
	}

	got := s.KnowledgeActivities("p1", 0)
	if len(got) != limit {
		t.Fatalf("p1 activity count = %d, want %d", len(got), limit)
	}
	if got[0].Query != fmt.Sprintf("q%d", limit+4) {
		t.Fatalf("newest activity = %q", got[0].Query)
	}
	if got[len(got)-1].Query != "q5" {
		t.Fatalf("oldest retained activity = %q, want q5", got[len(got)-1].Query)
	}
	if other := s.KnowledgeActivities("p2", 10); len(other) != 1 || other[0].Query != "keep" {
		t.Fatalf("another project's cap must not be affected: %#v", other)
	}
}

func TestKnowledgeActivityOrderUsesInsertion(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Same timestamp: the newest insert still sorts first.
	at := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := s.PutKnowledgeActivity(domain.KnowledgeActivity{
			At: at, Tool: "kb_search", ProjectID: "p1", Query: fmt.Sprintf("q%d", i), OK: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.KnowledgeActivities("p1", 10)
	if len(got) != 3 || got[0].Query != "q2" || got[2].Query != "q0" {
		t.Fatalf("activity order = %#v", got)
	}
}

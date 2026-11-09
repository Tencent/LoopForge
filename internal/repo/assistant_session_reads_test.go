package repo

import (
	"errors"
	"testing"

	"agentflow/internal/domain"
)

func TestAssistantSessionReadsFollowTheLatestReply(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutAssistantSession(11, domain.AssistantSession{ID: "chat", Title: "chat"}); err != nil {
		t.Fatal(err)
	}
	state := func(reader int64) AssistantGenerationState {
		t.Helper()
		return s.AssistantGenerationStatuses(reader, []string{"chat"})["chat"]
	}
	finish := func(g domain.AssistantGeneration) {
		t.Helper()
		if err := s.BeginAssistantGeneration(g); err != nil {
			t.Fatal(err)
		}
		if got := state(11); got.ID != g.ID || got.Status != "running" || got.FinishedAt != nil || got.Seen {
			t.Fatalf("a running reply is finished or seen: %+v", got)
		}
		g.Status = "completed"
		if err := s.CheckpointAssistantGeneration(g); err != nil {
			t.Fatal(err)
		}
		if got := state(11); got.Status != "completed" || got.FinishedAt == nil || got.Seen {
			t.Fatalf("a finished reply lost its finish time or is seen: %+v", got)
		}
	}

	finish(domain.AssistantGeneration{ID: "one", SessionID: "chat", UserID: 11, Status: "running"})
	if ok, err := s.MarkAssistantSessionRead(11, 11, "chat", "other"); ok || err != nil {
		t.Fatalf("a reply that is not the latest was recorded: %v %v", ok, err)
	}
	// A reader resolves conversations in the owner's namespace only.
	if ok, err := s.MarkAssistantSessionRead(12, 12, "chat", "one"); ok || !errors.Is(err, ErrAssistantReplyNotFound) {
		t.Fatalf("another user's reply was found in the reader's namespace: %v %v", ok, err)
	}
	if ok, err := s.MarkAssistantSessionRead(11, 11, "chat", "one"); !ok || err != nil {
		t.Fatalf("latest reply not recorded: %v %v", ok, err)
	}
	if !state(11).Seen || state(12).Seen {
		t.Fatal("read state is not per reader")
	}

	// The next reply starts unread again, and a stale tab cannot clear it.
	finish(domain.AssistantGeneration{ID: "two", SessionID: "chat", UserID: 11, Status: "running"})
	if ok, _ := s.MarkAssistantSessionRead(11, 11, "chat", "one"); ok || state(11).Seen {
		t.Fatal("an older reply cleared the newer one")
	}
	if ok, _ := s.MarkAssistantSessionRead(11, 11, "chat", "two"); !ok || !state(11).Seen {
		t.Fatal("newer reply not recorded")
	}
	if ok, err := s.MarkAssistantSessionRead(11, 11, "missing", "two"); ok || !errors.Is(err, ErrAssistantReplyNotFound) {
		t.Fatalf("a conversation without replies was recorded: %v %v", ok, err)
	}

	// Rows that ended before finish times were recorded stay unmarked.
	if err := s.DB.Exec("UPDATE assistant_generations SET finished_at = NULL WHERE session_id = ?", "chat").Error; err != nil {
		t.Fatal(err)
	}
	if state(12).FinishedAt != nil {
		t.Fatal("legacy reply reports a finish time")
	}

	if err := s.DeleteAssistantSession(11, "chat"); err != nil {
		t.Fatal(err)
	}
	var left int64
	if err := s.DB.Table("assistant_session_reads").Where("session_id = ?", "chat").Count(&left).Error; err != nil || left != 0 {
		t.Fatalf("deleted conversation kept read records: %d %v", left, err)
	}
}

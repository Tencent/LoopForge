package repo

import (
	"agentflow/internal/domain"
	"testing"
	"time"
)

func TestAssistantGenerationClaimCheckpointAndPrivacy(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := domain.AssistantGeneration{ID: "one", SessionID: "chat", UserID: 11, Status: "running"}
	if err = s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginAssistantGeneration(g); err == nil {
		t.Fatal("duplicate generation claimed")
	}
	if _, ok := s.AssistantGeneration(12, "chat"); ok {
		t.Fatal("other user accessed generation")
	}
	if err = s.CancelAssistantGeneration(12, "chat", "one"); err == nil {
		t.Fatal("other user cancelled generation")
	}
	if err = s.CancelAssistantGeneration(11, "chat", "one"); err != nil {
		t.Fatal(err)
	}
	g.Events = []domain.AssistantGenerationEvent{{Sequence: 1, Data: []byte(`{"kind":"status","text":"正在处理"}`)}}
	if err = s.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.AssistantGeneration(11, "chat")
	if !saved.CancelRequested || len(saved.Events) != 1 {
		t.Fatal("checkpoint lost cancellation or events")
	}
	g.Status = "cancelled"
	if err = s.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	g.ID = "two"
	g.Status = "running"
	if err = s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	stale := g
	stale.ID = "one"
	stale.Status = "completed"
	if err = s.CheckpointAssistantGeneration(stale); err == nil {
		t.Fatal("old worker overwrote new generation")
	}
	if err = s.DB.Model(&assistantGenerationRow{}).Where("session_id = ?", "chat").Update("updated_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	saved, _ = s.AssistantGeneration(11, "chat")
	if saved.Status != "interrupted" {
		t.Fatal("orphan generation did not become interrupted")
	}
}

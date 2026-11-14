package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAssistantDiagnosticsCheckpointUpsertAndHeartbeat(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const owner = 21
	if err := s.PutAssistantSession(owner, domain.AssistantSession{ID: "upsert-session"}); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "upsert-turn", SessionID: "upsert-session", UserID: owner, Status: "running", StartedAt: time.Now().UTC()}
	if err := s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	report := initialAssistantDiagnostics(g)
	report.Attempts = []domain.AssistantCLIAttempt{{ID: "upsert-turn/1", Engine: "codex"}}
	for i := 0; i < 2; i++ {
		if err := s.CheckpointAssistantGeneration(g, report); err != nil {
			t.Fatal(err)
		}
	}
	var before assistantDiagnosticsRow
	if err := s.DB.Where("generation_id = ?", g.ID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	s.DB.Model(&assistantDiagnosticsRow{}).Where("generation_id = ?", g.ID).Count(&count)
	if count != 1 {
		t.Fatalf("upsert created %d rows", count)
	}
	if err := s.HeartbeatAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	var after assistantDiagnosticsRow
	if err := s.DB.Where("generation_id = ?", g.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if string(after.Payload) != string(before.Payload) {
		t.Fatal("heartbeat rewrote the diagnostic payload")
	}
	stored, ok := s.AssistantDiagnostics(owner, g.SessionID, g.ID)
	if !ok || stored.Status != "running" || len(stored.Attempts) != 1 || stored.LastCheckpointAt == nil {
		t.Fatalf("checkpoint lost: %+v", stored)
	}
	g.Status = "completed"
	if err := s.HeartbeatAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatAssistantGeneration(g); err == nil {
		t.Fatal("heartbeat accepted a finished generation")
	}
}

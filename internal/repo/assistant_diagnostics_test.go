package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAssistantDiagnosticsSurviveRetryAndDeleteWithSession(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const owner = 11
	if err := s.PutAssistantSession(owner, domain.AssistantSession{ID: "diagnostic-history"}); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "first-diagnostic", SessionID: "diagnostic-history", UserID: owner, Status: "running", StartedAt: time.Now().UTC()}
	if err := s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	g.Status = "failed"
	g.Failure = &domain.AssistantFailure{Code: "NETWORK_ERROR", Message: "网络连接中断"}
	report := initialAssistantDiagnostics(g)
	report.Error = "connection reset"
	report.Attempts = []domain.AssistantCLIAttempt{{ID: "first/1", Engine: "codebuddy"}}
	if err := s.CheckpointAssistantGeneration(g, report); err != nil {
		t.Fatal(err)
	}
	first := g
	g.ID, g.Status, g.Failure = "second-diagnostic", "running", nil
	if err := s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckpointAssistantGeneration(first, report); err == nil {
		t.Fatal("stale checkpoint was allowed")
	}
	stored, ok := s.AssistantDiagnostics(owner, g.SessionID, first.ID)
	if !ok || stored.Status != "failed" || stored.Failure.Code != "NETWORK_ERROR" || len(stored.Attempts) != 1 || stored.Error != "connection reset" {
		t.Fatalf("retry replaced failure: %+v", stored)
	}
	if _, ok := s.AssistantDiagnostics(owner+1, g.SessionID, first.ID); ok {
		t.Fatal("owner scope bypassed")
	}
	if _, ok := s.AssistantDiagnostics(owner, "wrong-session", first.ID); ok {
		t.Fatal("session scope bypassed")
	}
	g.Status = "completed"
	if err := s.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAssistantSession(owner, g.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AssistantDiagnostics(owner, g.SessionID, first.ID); ok {
		t.Fatal("deleted conversation left diagnostic data")
	}
}

func TestAssistantDiagnosticsStaleRunningTurnsRefreshToCheckpointLost(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const owner = 12
	if err := s.PutAssistantSession(owner, domain.AssistantSession{ID: "stale-diag-session"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-2 * time.Minute).UTC()
	g := domain.AssistantGeneration{ID: "stale-turn-1", SessionID: "stale-diag-session", UserID: owner, Status: "running", StartedAt: started}
	if err := s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	partial := domain.AssistantDiagnostics{
		Version:      2,
		GenerationID: g.ID,
		SessionID:    g.SessionID,
		Status:       "running",
		StartedAt:    started,
		Environment:  &domain.AssistantDiagnosticEnvironment{PlatformVersion: "test", InstanceID: "api-crashed"},
		Attempts:     []domain.AssistantCLIAttempt{{ID: "stale-turn-1/1", Engine: "codex", CLIVersion: "0.163.0", Status: "running", Process: &domain.AssistantDiagnosticProcess{PID: 99, State: "running"}}},
		Tools:        []domain.AssistantDiagnosticTool{{ID: "gateway/1", Source: "gateway", Name: "tapd__bugs_list", Status: "running", StartedAt: started}},
	}
	if err := s.CheckpointAssistantGeneration(g, partial); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Minute).UTC()
	if err := s.DB.Exec("UPDATE assistant_generations SET updated_at = ? WHERE session_id = ?", expired, g.SessionID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("UPDATE assistant_diagnostics SET updated_at = ? WHERE generation_id = ?", expired, g.ID).Error; err != nil {
		t.Fatal(err)
	}
	gotGen, ok := s.AssistantGeneration(owner, g.SessionID)
	if !ok || gotGen.Status != "interrupted" {
		t.Fatalf("expected interrupted generation, got %+v", gotGen)
	}
	report, ok := s.AssistantDiagnostics(owner, g.SessionID, g.ID)
	if !ok {
		t.Fatal("missing diagnostics after interruption")
	}
	if report.Status != "interrupted" || report.Termination == nil || report.Termination.Code != "CHECKPOINT_LOST" {
		t.Fatalf("expected CHECKPOINT_LOST termination, got %+v", report)
	}
	if len(report.Tools) != 1 || report.Tools[0].Status != "unfinished" {
		t.Fatalf("lost pre-crash tool evidence: %+v", report.Tools)
	}
	if report.Assessment == nil || report.LastCheckpointAt == nil {
		t.Fatalf("missing assessment or lastCheckpointAt: %+v", report)
	}
}

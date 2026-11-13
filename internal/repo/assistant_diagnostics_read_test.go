package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAssistantDiagnosticsReadRefreshesStaleRunningReport(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const owner = 21
	if err := s.PutAssistantSession(owner, domain.AssistantSession{ID: "diag-read"}); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "diag-read-1", SessionID: "diag-read", UserID: owner, Status: "running", StartedAt: time.Now().UTC()}
	if err := s.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	report := domain.AssistantDiagnostics{Version: 2, GenerationID: g.ID, SessionID: g.SessionID, Status: "running", StartedAt: g.StartedAt,
		Attempts: []domain.AssistantCLIAttempt{{ID: g.ID + "/1", CLIVersion: "1", Process: &domain.AssistantDiagnosticProcess{PID: 5, State: "running"}}}}
	if err := s.CheckpointAssistantGeneration(g, report); err != nil {
		t.Fatal(err)
	}

	fresh, ok := s.AssistantDiagnostics(owner, g.SessionID, g.ID)
	if !ok || fresh.Status != "running" || fresh.Termination != nil || fresh.Failure != nil || fresh.Assessment == nil || fresh.Assessment.Outcome != "pending" {
		t.Fatalf("fresh running report must stay live: %+v", fresh)
	}

	// Read the diagnostics directly (without refreshing the generation slot
	// first): the read path itself must notice the missing heartbeat.
	stale := time.Now().Add(-46 * time.Second).UTC()
	if err := s.DB.Exec("UPDATE assistant_diagnostics SET payload = ?, updated_at = ? WHERE generation_id = ?",
		mustJSON(domain.AssistantDiagnostics{Version: 2, GenerationID: g.ID, SessionID: g.SessionID, Status: "running", StartedAt: g.StartedAt}), stale, g.ID).Error; err != nil {
		t.Fatal(err)
	}
	got, ok := s.AssistantDiagnostics(owner, g.SessionID, g.ID)
	if !ok {
		t.Fatal("stale diagnostics missing")
	}
	if got.Status != "interrupted" || got.Termination == nil || got.Termination.Code != "CHECKPOINT_LOST" || got.Termination.Source != "platform" {
		t.Fatalf("stale report not interrupted: %+v", got)
	}
	if got.Failure == nil || got.Failure.Code != "EXECUTION_INTERRUPTED" || got.Failure.Phase != "server" {
		t.Fatalf("stale report failure = %+v", got.Failure)
	}
	if got.LastCheckpointAt == nil || got.LastCheckpointAt.Sub(stale).Abs() > time.Second {
		t.Fatalf("lastCheckpointAt must fall back to the row heartbeat %v, got %v", stale, got.LastCheckpointAt)
	}
	if got.Assessment == nil || got.Assessment.Outcome != "termination_recorded" {
		t.Fatalf("stale report assessment = %+v", got.Assessment)
	}

	// Existing termination, failure and checkpoint evidence is never replaced.
	kept := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	original := domain.AssistantDiagnostics{Version: 2, GenerationID: g.ID, SessionID: g.SessionID, Status: "running", LastCheckpointAt: &kept,
		Termination: &domain.AssistantDiagnosticTermination{Code: "USER_STEER", Message: "用户改了方向"},
		Failure:     &domain.AssistantFailure{Code: "NETWORK_ERROR", Message: "网络连接中断"}}
	if err := s.DB.Exec("UPDATE assistant_diagnostics SET payload = ?, updated_at = ? WHERE generation_id = ?", mustJSON(original), stale, g.ID).Error; err != nil {
		t.Fatal(err)
	}
	got, ok = s.AssistantDiagnostics(owner, g.SessionID, g.ID)
	if !ok || got.Status != "interrupted" || got.Termination.Code != "USER_STEER" || got.Failure.Code != "NETWORK_ERROR" || !got.LastCheckpointAt.Equal(kept) {
		t.Fatalf("stale refresh overwrote recorded evidence: %+v", got)
	}

	if err := s.DB.Exec("UPDATE assistant_diagnostics SET payload = ? WHERE generation_id = ?", []byte("{not json"), g.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AssistantDiagnostics(owner, g.SessionID, g.ID); ok {
		t.Fatal("corrupt payload must not be returned")
	}
}

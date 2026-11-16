package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A lease is recorded only on the running turn it belongs to. A late callback
// from a finished turn, or one naming another turn of the same conversation,
// must not leave a token that a later reap would release.
func TestRecordAssistantGenerationWorkspaceLeaseOnlyOnItsRunningTurn(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recorded := func() (string, string) {
		t.Helper()
		var row assistantGenerationRow
		if err := s.DB.Where("session_id = ?", "fenced-chat").Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row.WorkspaceRunID, row.WorkspaceLease
	}
	first := domain.AssistantGeneration{ID: "first", SessionID: "fenced-chat", UserID: 11, Status: "running"}
	if err = s.BeginAssistantGeneration(first); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAssistantGenerationWorkspaceLease(11, "fenced-chat", "other-turn", "run-1", "stray"); err != nil {
		t.Fatal(err)
	}
	if run, lease := recorded(); run != "" || lease != "" {
		t.Fatalf("a lease for another turn was recorded: %q %q", run, lease)
	}
	first.Status = "completed"
	if err = s.CheckpointAssistantGeneration(first); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAssistantGenerationWorkspaceLease(11, "fenced-chat", "first", "run-1", "late"); err != nil {
		t.Fatal(err)
	}
	if run, lease := recorded(); run != "" || lease != "" {
		t.Fatalf("a finished turn recorded a lease: %q %q", run, lease)
	}

	// The next turn starts clean and records only its own lease.
	next := domain.AssistantGeneration{ID: "next", SessionID: "fenced-chat", UserID: 11, Status: "running"}
	if err = s.BeginAssistantGeneration(next); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAssistantGenerationWorkspaceLease(11, "fenced-chat", "next", "run-2", "own"); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.Model(&assistantGenerationRow{}).Where("session_id = ?", "fenced-chat").Update("updated_at", time.Now().Add(-time.Minute).UTC()).Error; err != nil {
		t.Fatal(err)
	}
	if _, lease, ok := s.ReapAssistantGeneration(11, "fenced-chat"); !ok || lease != (AssistantGenerationLease{RunID: "run-2", Token: "own"}) {
		t.Fatalf("reap returned %+v", lease)
	}
}

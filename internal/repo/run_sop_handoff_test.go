package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestSOPHandoffSourceFencesChatInsideRestoreTransaction(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkSOPHandoffFences(t, s)
}
func TestMySQLSOPHandoffSourceFencesChatInsideRestoreTransaction(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("local isolated MySQL required")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkSOPHandoffFences(t, s)
}
func checkSOPHandoffFences(t *testing.T, s *Store) {
	t.Helper()
	run := domain.Run{ID: fmt.Sprintf("handoff-cas-%d", time.Now().UnixNano()), Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group"}, Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	chat, err := s.EnsureRunAssistantSession(run.ID, "p", "chat", 7)
	if err != nil {
		t.Fatal(err)
	}
	source := SOPHandoffSessionSource(chat)
	run, _ = s.RunState(run.ID)
	next := run
	next.WorkflowSnapshot.CollaborationMode = ""
	chat.Messages = append(chat.Messages, domain.AssistantMessage{Role: "user", Content: "late correction"})
	if err := s.PutAssistantSession(chat.UserID, chat); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreRunSOP(next, run.Revision, false, source); err == nil {
		t.Fatal("lost late correction")
	}
	got, _ := s.RunState(run.ID)
	if got.WorkflowSnapshot.CollaborationMode != "group" {
		t.Fatal("transaction did not roll back")
	}
	chat, _ = s.SharedAssistantSession(chat.ID)
	if err := s.BeginAssistantGeneration(domain.AssistantGeneration{ID: "new-turn", SessionID: chat.ID, UserID: chat.UserID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreRunSOP(next, run.Revision, false, SOPHandoffSessionSource(chat)); err == nil {
		t.Fatal("switched during Main Bot generation")
	}
	g, _ := s.AssistantGeneration(chat.UserID, chat.ID)
	g.Status = "completed"
	if err := s.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreRunSOP(next, run.Revision, false, SOPHandoffSessionSource(chat)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.RunState(run.ID)
	if got.WorkflowSnapshot.CollaborationMode != "" {
		t.Fatal("valid handoff did not commit")
	}
}

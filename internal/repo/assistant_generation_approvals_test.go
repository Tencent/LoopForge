package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNativeApprovalsUseGenerationStateAndSurviveCheckpoints(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.DB.Migrator().HasTable("assistant_native_approvals") {
		t.Fatal("separate approval table was created")
	}
	g := domain.AssistantGeneration{ID: "approval-generation", SessionID: "approval-control", UserID: 41, ActorID: 42, Status: "running", StartedAt: time.Now().Add(-24 * time.Hour)}
	if err := st.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	item := domain.AssistantNativeApproval{ID: "exact-one", GenerationID: g.ID, ActorID: 42, Tool: "Bash", Input: json.RawMessage(`{"command":"printf private-argument"}`)}
	if err := st.CreateNativeApproval(41, g.SessionID, "phase-a", item); err != nil {
		t.Fatal(err)
	}
	// The recorder's copy predates both the callback and the browser decision.
	if err := st.DecideNativeApproval(41, 42, g.SessionID, item.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	g.Events = []domain.AssistantGenerationEvent{{Sequence: 1, Data: json.RawMessage(`{"kind":"thinking"}`)}}
	if err := st.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNativeApproval(41, g.SessionID, "phase-a", item); err != nil {
		t.Fatal(err)
	}
	got, ok := st.NativeApproval(41, g.SessionID, item.ID)
	if !ok || got.Decision != "allow" {
		t.Fatal("checkpoint or retry lost the decision", got)
	}
	visible, _ := st.AssistantGeneration(41, g.SessionID)
	raw, _ := json.Marshal(visible)
	if strings.Contains(string(raw), "private-argument") || strings.Contains(string(raw), "nativeApprovals") {
		t.Fatal("generation DTO exposed private tool input")
	}
	changed := item
	changed.Input = json.RawMessage(`{"command":"different"}`)
	if err := st.CreateNativeApproval(41, g.SessionID, "phase-a", changed); err == nil {
		t.Fatal("approval input was replaced")
	}
	if err := st.CreateNativeApproval(41, g.SessionID, "phase-b", item); err == nil {
		t.Fatal("another CLI phase reused an invocation")
	}
	item.ID = "exact-two"
	if err := st.CreateNativeApproval(41, g.SessionID, "phase-b", item); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseNativeApprovals(41, g.SessionID, g.ID, "phase-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.NativeApproval(41, g.SessionID, "exact-one"); ok {
		t.Fatal("exited phase kept its permit")
	}
	if items := st.PendingNativeApprovals(41, g.SessionID); len(items) != 1 || items[0].ID != "exact-two" {
		t.Fatal("sibling phase lost pending request", items)
	}
	g.Status = "completed"
	if err := st.CheckpointAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	var row assistantGenerationRow
	st.DB.Where("session_id = ?", g.SessionID).Take(&row)
	if len(generationPayload(row).NativeApprovals) != 0 {
		t.Fatal("completed generation retained approval control state")
	}
}

func TestNativeApprovalTrafficCannotKeepDeadGenerationAlive(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	g := domain.AssistantGeneration{ID: "dead-worker", SessionID: "dead-approval", UserID: 41, Status: "running"}
	if err := st.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	item := domain.AssistantNativeApproval{ID: "waiting", GenerationID: g.ID, ActorID: 41, Tool: "Write", Input: json.RawMessage(`{}`)}
	if err := st.CreateNativeApproval(41, g.SessionID, "phase", item); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := st.DB.Model(&assistantGenerationRow{}).Where("session_id = ?", g.SessionID).UpdateColumn("updated_at", old).Error; err != nil {
		t.Fatal(err)
	}
	if items := st.PendingNativeApprovals(41, g.SessionID); len(items) != 0 {
		t.Fatal("dead worker's request remained active")
	}
	if err := st.DecideNativeApproval(41, 41, g.SessionID, item.ID, "allow"); err == nil {
		t.Fatal("dead worker was approved")
	}
	item.ID = "new"
	if err := st.CreateNativeApproval(41, g.SessionID, "phase", item); err == nil {
		t.Fatal("callback revived dead generation")
	}
}

func TestMySQLNativeApprovalsShareGenerationControlState(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("local isolated MySQL required")
	}
	a, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	session := fmt.Sprintf("native-approval-%d", time.Now().UnixNano())
	g := domain.AssistantGeneration{ID: session, SessionID: session, UserID: 41, ActorID: 42, Status: "running"}
	defer a.DB.Where("session_id = ?", session).Delete(&assistantGenerationRow{})
	if err := a.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	item := domain.AssistantNativeApproval{ID: "cross-instance", GenerationID: g.ID, ActorID: 42, Tool: "Write", Input: json.RawMessage(`{"path":"exact.txt"}`)}
	if err := a.CreateNativeApproval(41, session, "phase", item); err != nil {
		t.Fatal(err)
	}
	if items := b.PendingNativeApprovals(41, session); len(items) != 1 {
		t.Fatal("other API instance cannot refresh request", items)
	}
	checkpointed := make(chan error, 1)
	go func() {
		for i := 0; i < 30; i++ {
			if err := a.CheckpointAssistantGeneration(g); err != nil {
				checkpointed <- err
				return
			}
		}
		checkpointed <- nil
	}()
	if err := b.DecideNativeApproval(41, 42, session, item.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	if err := <-checkpointed; err != nil {
		t.Fatal(err)
	}
	if current, ok := a.NativeApproval(41, session, item.ID); !ok || current.Decision != "allow" {
		t.Fatal("concurrent checkpoint lost another API instance's decision", current)
	}
	if err := b.CancelAssistantGeneration(41, session, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.NativeApproval(41, session, item.ID); ok {
		t.Fatal("cancel from other instance did not revoke approval")
	}
}

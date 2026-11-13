package repo

import (
	"agentflow/internal/domain"
	"errors"
	"testing"
	"time"
)

func TestConversationExecutionBindingIsImmutableAndLegacyIsCloud(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	legacy := domain.AssistantSession{ID: "legacy-machine", UserID: 41}
	if err = st.DB.Create(&assistantSessionRow{ID: legacy.ID, UserID: 41, Payload: mustJSON(legacy)}).Error; err != nil {
		t.Fatal(err)
	}
	got, _ := st.AssistantSession(41, legacy.ID)
	if got.Execution().Pool != domain.ExecutionModeCloud || got.Execution().RunnerID != "" {
		t.Fatal(got.Execution())
	}
	got.ExecutionTarget = &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: "remote"}
	if err = st.PutAssistantSession(41, got); !errors.Is(err, ErrAssistantExecutionLocked) {
		t.Fatalf("legacy rebind: %v", err)
	}
	got.ID = "new-machine"
	if err = st.PutAssistantSession(41, got); err != nil {
		t.Fatal(err)
	}
	got.ExecutionTarget = nil
	if err = st.PutAssistantSession(41, got); err != nil {
		t.Fatal(err)
	}
	saved, _ := st.AssistantSession(41, got.ID)
	if saved.Execution().RunnerID != "remote" {
		t.Fatal("omitted target cleared binding")
	}
	got.ExecutionTarget = &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}
	if err = st.PutAssistantSession(41, got); !errors.Is(err, ErrAssistantExecutionLocked) {
		t.Fatalf("remote rebind: %v", err)
	}
}

func TestConversationModeSharesGenerationFence(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := domain.AssistantSession{ID: "mode-fence", WorkspaceRunID: "workspace"}
	if err = st.PutAssistantSession(41, s); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "generation", SessionID: s.ID, UserID: 41}
	if err = st.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err = st.SetAssistantRuntimeMode(41, s.ID, "", domain.AssistantModeWorkspace); err == nil {
		t.Fatal("external mode change raced running turn")
	}
	if err = st.SetAssistantRuntimeMode(41, s.ID, "", domain.AssistantModeWorkspace, g.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.CancelAssistantGeneration(41, s.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.SetAssistantRuntimeMode(41, s.ID, "", domain.AssistantModePlatform, g.ID); err == nil {
		t.Fatal("cancelled turn changed mode")
	}
}

func TestNativeApprovalIsExactActorAndLiveGeneration(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := domain.AssistantSession{ID: "approval-session"}
	if err = st.PutAssistantSession(41, s); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "g-approval", SessionID: s.ID, UserID: 41}
	if err = st.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	item := domain.AssistantNativeApproval{ID: "a1", ActorID: 42, GenerationID: g.ID, Tool: "Bash", Input: []byte(`{"command":"printf approved"}`), ExpiresAt: time.Now().Add(time.Minute)}
	if err = st.CreateNativeApproval(41, s.ID, item); err != nil {
		t.Fatal(err)
	}
	if err = st.DecideNativeApproval(41, 41, s.ID, item.ID, "allow"); err == nil {
		t.Fatal("different actor approved")
	}
	if err = st.DecideNativeApproval(41, 42, s.ID, item.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	if err = st.DecideNativeApproval(41, 42, s.ID, item.ID, "deny"); err == nil {
		t.Fatal("approval changed decision")
	}
	item.ID = "a2"
	if err = st.CreateNativeApproval(41, s.ID, item); err != nil {
		t.Fatal(err)
	}
	if err = st.CancelAssistantGeneration(41, s.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	if len(st.PendingNativeApprovals(41, s.ID)) != 0 {
		t.Fatal("cancelled approval remained visible")
	}
	if err = st.DecideNativeApproval(41, 42, s.ID, item.ID, "allow"); err == nil {
		t.Fatal("cancelled generation approved")
	}
	item.ID = "a3"
	item.GenerationID = ""
	item.ExpiresAt = time.Now().Add(-time.Second)
	if err = st.CreateNativeApproval(41, s.ID, item); err != nil {
		t.Fatal(err)
	}
	if err = st.DecideNativeApproval(41, 42, s.ID, item.ID, "allow"); err == nil {
		t.Fatal("expired action approved")
	}
}

func TestNewRunConversationAndForkInheritExecutionTarget(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	target := domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: "remote-runner"}
	s, err := st.EnsureRunAssistantSession("new-run", "platform", "run", 41, target)
	if err != nil {
		t.Fatal(err)
	}
	if s.Execution() != target {
		t.Fatalf("new run target: %+v", s.Execution())
	}
	branch := domain.AssistantSession{ID: "fork-target", UserID: 41, ParentSessionID: s.ID}
	if err = st.CreateAssistantSessionBranch(s, branch); err != nil {
		t.Fatal(err)
	}
	got, _ := st.AssistantSession(41, branch.ID)
	if got.Execution() != target {
		t.Fatal("fork lost target")
	}
	old, err := st.EnsureRunAssistantSession("old-run", "platform", "run", 41)
	if err != nil {
		t.Fatal(err)
	}
	got, err = st.EnsureRunAssistantSession("old-run", "platform", "run", 41, target)
	if err != nil {
		t.Fatal(err)
	}
	if got.Execution() != old.Execution() {
		t.Fatal("existing run conversation was rebound")
	}
}

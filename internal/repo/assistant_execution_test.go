package repo

import (
	"agentflow/internal/domain"
	"errors"
	"testing"
)

func TestConversationExecutionBindingIsImmutableAndLegacyIsCloud(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	legacy := domain.AssistantSession{ID: "legacy-machine", UserID: 41, Messages: []domain.AssistantMessage{{Role: "user", Content: "started"}}}
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

func TestConversationDraftSelectsUntilFirstGenerationAndKeepsFenceAfterHistoryClear(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := domain.AssistantSession{ID: "draft-machine", ExecutionTarget: &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}}
	if err = st.PutAssistantSession(41, s); err != nil {
		t.Fatal(err)
	}
	s, _ = st.AssistantSession(41, s.ID)
	if s.ExecutionLocked() {
		t.Fatal("blank draft locked")
	}
	s.ExecutionTarget = &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: "remote"}
	if err = st.PutAssistantSession(41, s); err != nil {
		t.Fatal(err)
	}
	s.ExecutionTarget = nil
	if err = st.PutAssistantSession(41, s); err != nil {
		t.Fatal(err)
	}
	s, _ = st.AssistantSession(41, s.ID)
	if s.Execution().RunnerID != "remote" || s.ExecutionLocked() {
		t.Fatal("draft binding lost or locked")
	}
	s.ExecutionTarget = &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}
	s.Messages = []domain.AssistantMessage{{Role: "user", Content: "first turn"}}
	if err = st.BeginAssistantGeneration(domain.AssistantGeneration{ID: "first", SessionID: s.ID, UserID: 41}, s); err != nil {
		t.Fatal(err)
	}
	s, _ = st.AssistantSession(41, s.ID)
	if !s.ExecutionLocked() || s.Execution().Pool != domain.ExecutionModeCloud {
		t.Fatal("first generation did not bind final choice")
	}
	s.Messages = nil
	s.ExecutionTargetLocked = false
	if err = st.PutAssistantSession(41, s); err != nil {
		t.Fatal(err)
	}
	s.ExecutionTarget = &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: "remote"}
	if err = st.PutAssistantSession(41, s); !errors.Is(err, ErrAssistantExecutionLocked) {
		t.Fatalf("history clear removed fence: %v", err)
	}
}

func TestConversationSummaryRetainsLegacyExecutionFence(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	legacy := domain.AssistantSession{ID: "legacy-list", UserID: 41, Messages: []domain.AssistantMessage{{Role: "user", Content: "hello"}}}
	if err = st.DB.Create(&assistantSessionRow{ID: legacy.ID, UserID: 41, Payload: mustJSON(legacy)}).Error; err != nil {
		t.Fatal(err)
	}
	for _, fallback := range []bool{false, true} {
		noSummaryProjection.Store(fallback)
		rows := st.ListAssistantSessions(41)
		if len(rows) != 1 || !rows[0].ExecutionLocked() || rows[0].Messages != nil {
			t.Fatalf("fallback=%v: %+v", fallback, rows)
		}
	}
	noSummaryProjection.Store(false)
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
	item := domain.AssistantNativeApproval{ID: "a1", ActorID: 42, GenerationID: g.ID, Tool: "Bash", Input: []byte(`{"command":"printf approved"}`)}
	if err = st.CreateNativeApproval(41, s.ID, "phase", item); err != nil {
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
	if err = st.CreateNativeApproval(41, s.ID, "phase", item); err != nil {
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
	if err = st.CreateNativeApproval(41, s.ID, "phase", item); err == nil {
		t.Fatal("unbound action created")
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

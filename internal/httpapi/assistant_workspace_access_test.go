package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func workspaceAccessGateway(t *testing.T, h *assistantHandlers) *agentToolGateway {
	t.Helper()
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.deps.engine = en
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = New(h.repo, en, tracker.NewService(), "", "")
	return gateway
}

// A viewer must not reach an SOP workspace through workspaceRunId: the run's
// own workspace edits, terminal, resume and retry already require write access.
func TestWorkspaceOpenRefusesSOPWorkspaceForViewer(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	h, owner, _ := chatSandboxFixture(t)
	gateway := workspaceAccessGateway(t, h)

	if err := h.repo.PutUser(domain.User{LoginName: "ws-viewer", CodeBuddyAPIKey: "test-only"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := h.repo.UserByLoginName("ws-viewer")
	project, _ := h.repo.Project("workbench")
	project.Members = append(project.Members, domain.ProjectMember{UserID: viewer.ID, Role: "viewer"})
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	snap := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "business", Name: "Business", Driver: domain.SandboxDriverDocker}}
	sop := domain.Run{
		ID:               "viewer-sop",
		Status:           "failed",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", IssueID: "iss-viewer", ExecutionMode: "cloud"},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", SandboxProfileID: "business", SandboxSnapshot: snap},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := h.repo.PutRun(sop); err != nil {
		t.Fatal(err)
	}
	seedSavedSOPCheckpoint(t, h.repo, sop.ID, map[string]string{"a.txt": "x\n"})
	t.Cleanup(func() { waitWorkspaceCheckpointSettled(t, h.repo, sop.ID) })

	// The existing policy already treats this viewer as read-only for the run.
	c, _ := workbenchContext(viewer, "POST", "/api/assistant/generations", bytes.NewBuffer(nil))
	req := assistantRequest{ProjectID: "workbench", RunID: sop.ID, Messages: []assistantMessage{{Role: "user", Content: "edit"}}}
	if err := h.prepareWorkbenchContext(c, &req, viewer); err == nil {
		t.Fatal("precondition: viewer must be refused in the SOP task conversation")
	}

	if err := h.repo.PutAssistantSession(viewer.ID, domain.AssistantSession{ID: "viewer-chat", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: viewer.ID, User: viewer, ChatSessionID: "viewer-chat", ChatOwnerID: viewer.ID}
	raw, err := gateway.openConversationWorkspace(context.Background(), s, map[string]any{"workspaceRunId": sop.ID})
	if err == nil {
		var out map[string]any
		_ = json.Unmarshal([]byte(raw), &out)
		t.Fatalf("viewer bound SOP workspace %s: handoff=%v requestedMode=%q", sop.ID, out["handoff"], s.requestedMode())
	}
	if !strings.Contains(err.Error(), "只读成员") {
		t.Fatalf("expected a read-only refusal, got %v", err)
	}
	if s.workspaceRunID != "" || s.requestedMode() != "" {
		t.Fatalf("refusal must not bind or switch modes: run=%q mode=%q", s.workspaceRunID, s.requestedMode())
	}
	if stored, _ := h.repo.AssistantSession(viewer.ID, "viewer-chat"); stored.WorkspaceRunID != "" {
		t.Fatalf("refusal stored binding %q", stored.WorkspaceRunID)
	}

	// A shared conversation another member bound to the SOP: a viewer speaking
	// there inherits the binding but may not enter it.
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "shared-sop-chat", ProjectID: "workbench", WorkspaceRunID: sop.ID, RuntimeMode: domain.AssistantModeWorkspace}); err != nil {
		t.Fatal(err)
	}
	conv, _ := h.repo.AssistantSession(owner.ID, "shared-sop-chat")
	inherited := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: viewer.ID, User: viewer, ChatSessionID: "shared-sop-chat", ChatOwnerID: owner.ID}
	loadTurnWorkspace(h.deps, inherited, conv)
	if inherited.workspaceRunID != sop.ID {
		t.Fatalf("precondition: the turn loads the shared binding, got %q", inherited.workspaceRunID)
	}
	if _, err := gateway.openConversationWorkspace(context.Background(), inherited, map[string]any{}); err == nil || !strings.Contains(err.Error(), "只读成员") {
		t.Fatalf("viewer entered the inherited SOP workspace: %v", err)
	}
	if stored, _ := h.repo.AssistantSession(owner.ID, "shared-sop-chat"); stored.WorkspaceRunID != sop.ID {
		t.Fatalf("refusal changed the shared binding to %q", stored.WorkspaceRunID)
	}
	vc, _ := workbenchContext(viewer, "POST", "/api/assistant/generations", bytes.NewBuffer(nil))
	if !h.sopWorkspaceReadOnly(vc, conv, "workbench", viewer.ID) {
		t.Fatal("a viewer turn must start the shared SOP conversation in platform mode")
	}
	oc, _ := workbenchContext(owner, "POST", "/api/assistant/generations", bytes.NewBuffer(nil))
	if h.sopWorkspaceReadOnly(oc, conv, "workbench", owner.ID) {
		t.Fatal("the owner keeps workspace mode in the shared SOP conversation")
	}

	// The owner still binds the same SOP workspace.
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "owner-chat", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	ownerSession := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "owner-chat", ChatOwnerID: owner.ID}
	raw, err = gateway.openConversationWorkspace(context.Background(), ownerSession, map[string]any{"workspaceRunId": sop.ID})
	if err != nil {
		t.Fatalf("owner open: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(raw), &out)
	if out["handoff"] != true || ownerSession.workspaceRunID != sop.ID {
		t.Fatalf("owner did not bind SOP workspace: %s", raw)
	}
}

// Reopening the bound workspace while preparing it fails transiently (lease
// held elsewhere) keeps the binding instead of silently unbinding it.
func TestWorkspaceOpenReusedPrepFailureKeepsBinding(t *testing.T) {
	h, owner, _ := chatSandboxFixture(t)
	gateway := workspaceAccessGateway(t, h)
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "sandbox-chat", ChatOwnerID: owner.ID}

	if _, err := gateway.openConversationWorkspace(context.Background(), s, map[string]any{"sandboxProfileId": "business"}); err != nil {
		t.Fatal(err)
	}
	own := s.workspaceRunID
	if own == "" {
		t.Fatal("no workspace bound after first open")
	}
	waitWorkspaceCheckpointSettled(t, h.repo, own)

	// Another API instance, IDE or save holds the workspace lease.
	token, err := h.repo.BeginWorkspaceUse(own, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, openErr := gateway.openConversationWorkspace(context.Background(), s, map[string]any{})
	if err := h.repo.FinishWorkspaceUse(own, token, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if openErr == nil {
		t.Fatal("precondition: open must fail while the lease is held")
	}
	if strings.Contains(openErr.Error(), "已恢复原绑定") || !strings.Contains(openErr.Error(), own) {
		t.Fatalf("error must report the kept binding, got %v", openErr)
	}
	stored, _ := h.repo.AssistantSession(owner.ID, "sandbox-chat")
	if s.workspaceRunID != own || stored.WorkspaceRunID != own {
		t.Fatalf("transient prep failure unbound current workspace: session=%q stored=%q want %q", s.workspaceRunID, stored.WorkspaceRunID, own)
	}
}

// A legacy SOP run (no frozen execution mode) ran on its owner's default
// runner, so a cloud conversation may not borrow it.
func TestWorkspaceOpenLegacySOPUsesEffectiveRunner(t *testing.T) {
	h, owner, _ := chatSandboxFixture(t)
	gateway := workspaceAccessGateway(t, h)
	if err := h.repo.PutRunner(domain.Runner{ID: "legacy-runner", Name: "旧机器", OwnerUserID: owner.ID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationWorkspace}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetDefaultRunner(owner.ID, "legacy-runner", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := h.repo.DefaultRunner(owner.ID); got != "legacy-runner" {
		t.Fatalf("precondition: default runner %q", got)
	}
	snap := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "business", Driver: domain.SandboxDriverDocker}}
	legacy := domain.Run{
		ID:               "legacy-sop",
		Status:           "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ResponsibleUserID: owner.ID},
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "business", SandboxSnapshot: snap},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := h.repo.PutRun(legacy); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "legacy-cloud-chat", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "legacy-cloud-chat", ChatOwnerID: owner.ID}
	_, err := gateway.openConversationWorkspace(context.Background(), s, map[string]any{"workspaceRunId": legacy.ID})
	if err == nil || !strings.Contains(err.Error(), "legacy-runner") || !strings.Contains(err.Error(), "不在同一执行机器") {
		t.Fatalf("expected machine mismatch for a legacy run on the default runner, got %v", err)
	}
	if s.workspaceRunID != "" {
		t.Fatalf("mismatch must not bind, got %q", s.workspaceRunID)
	}
}

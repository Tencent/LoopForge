package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/application"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
)

func chatSandboxFixture(t *testing.T) (*assistantHandlers, domain.User, *AgentToolSession) {
	t.Helper()
	h, user := workbenchFixture(t)
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	recipe := &domain.SandboxDockerfile{Content: "FROM alpine:3.20\n"}
	for _, profile := range []domain.SandboxProfile{
		{ID: "business", Name: "Business", ProjectID: "workbench", Driver: "docker", Dockerfile: recipe},
		{ID: "shared", Name: "Shared", ProjectID: "other", Shared: true, Driver: "docker", Dockerfile: recipe},
		{ID: "private", Name: "Private", ProjectID: "other", Driver: "docker", Dockerfile: recipe},
	} {
		if err := h.repo.PutSandbox(profile); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.repo.PutRepository(domain.Repository{ID: "chat-repo", Name: "web", ProjectID: "workbench", DefaultBranch: "main", LocalPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: "sandbox-chat", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: user.ID, User: user, ChatSessionID: "sandbox-chat", ChatOwnerID: user.ID}
	return h, user, s
}

func TestConversationSandboxSelectionFreezesIndependentWorkspace(t *testing.T) {
	h, user, s := chatSandboxFixture(t)
	gateway := agentToolGateway{deps: h.deps}
	open := func(sandbox string) (domain.Run, bool, error) {
		bound, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{SandboxID: sandbox})
		return bound.Run, bound.Reused, err
	}
	if _, _, err := open("private"); err == nil || len(h.repo.Runs()) != 0 {
		t.Fatalf("another project's private sandbox accepted: %v", err)
	}
	description := gateway.workspaceOpenDescriptorFor(s).Description
	if !strings.Contains(description, "business：Business") || !strings.Contains(description, "shared：Shared") || strings.Contains(description, "private") || !strings.Contains(description, `"name":"web"`) {
		t.Fatalf("choices not offered to the model: %s", description)
	}
	run, reused, err := open("business")
	if err != nil || reused {
		t.Fatalf("open: %v %v", reused, err)
	}
	if !run.IsWorkspace() || run.WorkflowID != domain.ConversationWorkspaceWorkflowID || run.WorkflowSnapshot.CollaborationMode != "" || run.Status != "waiting" || len(run.Nodes) != 0 {
		t.Fatalf("not an independent idle workspace: %+v", run)
	}
	if run.WorkflowSnapshot.SandboxProfileID != "business" || run.WorkflowSnapshot.SandboxSnapshot.Profile.Driver != "docker" {
		t.Fatalf("wrong environment: %+v", run.WorkflowSnapshot.SandboxSnapshot)
	}
	frozen := run.WorkflowSnapshot.SandboxSnapshot.Dockerfile.Content
	business, _ := h.repo.Sandbox("business")
	business.Dockerfile = &domain.SandboxDockerfile{Content: "FROM busybox:1.36\n"}
	_ = h.repo.PutSandbox(business)
	again, reused, err := open("")
	if err != nil || !reused || again.ID != run.ID || again.WorkflowSnapshot.SandboxProfileID != "business" || again.WorkflowSnapshot.SandboxSnapshot.Dockerfile.Content != frozen {
		t.Fatalf("frozen environment changed: %+v %v", again, err)
	}
	// The preference only applies to workspaces opened later.
	if err := h.repo.SetAssistantSessionSandbox(user.ID, s.ChatSessionID, "shared"); err != nil {
		t.Fatalf("environment preference rejected once a workspace exists: %v", err)
	}
	// The user's own choice in advanced settings wins over the model's.
	s.ChatSessionID = "override-chat"
	s.workspaceRunID = ""
	s.SandboxProfileID = "business"
	_ = h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: s.ChatSessionID, ProjectID: s.ProjectID, SandboxProfileID: "business"})
	if !strings.Contains(gateway.workspaceOpenDescriptorFor(s).Description, "用户已在高级设置中指定运行环境 business") {
		t.Fatal("model not told about the user's fixed environment")
	}
	override, _, err := open("shared")
	if err != nil || override.WorkflowSnapshot.SandboxProfileID != "business" {
		t.Fatalf("user selection ignored: %v", err)
	}
}

// The preferred sandbox kind in the AI assistant settings leads the choices
// and opens when the agent names no environment; it never overrides an
// explicit choice.
func TestConversationSandboxPreferenceGuidesTheAgent(t *testing.T) {
	h, user, s := chatSandboxFixture(t)
	gateway := agentToolGateway{deps: h.deps}
	prefer := func(driver string) int {
		t.Helper()
		c, rec := workbenchContext(user, "PUT", "/api/assistant/preferences", bytes.NewBufferString(`{"workspaceDriver":"`+driver+`"}`))
		h.preferences(c)
		return rec.Code
	}
	if code := prefer("seatbelt"); code != 400 {
		t.Fatalf("unknown workspace driver accepted: %d", code)
	}
	description := gateway.workspaceOpenDescriptorFor(s).Description
	if !strings.Contains(description, "拿不准用 generic-docker") || strings.Contains(description, "倾向") {
		t.Fatalf("no preference still steers the agent: %s", description)
	}
	if code := prefer("native"); code != 200 {
		t.Fatalf("save preference: %d", code)
	}
	s.ExecutionRunnerID = "sandbox-preference-runner"
	if err := h.repo.PutRunner(domain.Runner{ID: s.ExecutionRunnerID, OS: "darwin", RunnerVersion: domain.RunnerVersionConversationWorkspace, NativeSandboxAvailable: true}); err != nil {
		t.Fatal(err)
	}
	target := &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: s.ExecutionRunnerID}
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: s.ChatSessionID, ProjectID: s.ProjectID, ExecutionTarget: target}); err != nil {
		t.Fatal(err)
	}
	description = gateway.workspaceOpenDescriptorFor(s).Description
	native := repo.TestNativeSandboxID
	if !strings.Contains(description, "倾向 native 沙箱") || !strings.Contains(description, "没有特殊要求时用 "+native) ||
		strings.Index(description, "- "+native+"：") > strings.Index(description, "- business：") {
		t.Fatalf("preferred sandbox not offered first: %s", description)
	}
	bound, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{})
	if err != nil || bound.Run.WorkflowSnapshot.SandboxProfileID != native {
		t.Fatalf("default environment ignores the preference: %+v %v", bound.Run.WorkflowSnapshot, err)
	}
	s.ChatSessionID, s.workspaceRunID = "explicit-chat", ""
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: s.ChatSessionID, ProjectID: s.ProjectID, ExecutionTarget: target}); err != nil {
		t.Fatal(err)
	}
	bound, err = gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{SandboxID: "business"})
	if err != nil || bound.Run.WorkflowSnapshot.SandboxProfileID != "business" {
		t.Fatalf("the agent's explicit choice was overridden: %+v %v", bound.Run.WorkflowSnapshot, err)
	}
	if code := prefer("local"); code != 400 {
		t.Fatalf("the removed local kind accepted as a preference: %d", code)
	}
	if err := h.repo.PutRunner(domain.Runner{ID: s.ExecutionRunnerID, OS: "linux", RunnerVersion: domain.RunnerVersionConversationWorkspace}); err != nil {
		t.Fatal(err)
	}
	if _, preferred := gateway.conversationSandboxChoices(s); preferred != "" {
		t.Fatalf("native preference selected %q on a runner without bwrap", preferred)
	}
	t.Setenv("PATH", t.TempDir()) // A Compose installation lacks a native sandbox binary.
	s.ExecutionRunnerID = ""
	s.ChatSessionID, s.workspaceRunID = "docker-fallback-chat", ""
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: s.ChatSessionID, ProjectID: s.ProjectID}); err != nil {
		t.Fatal(err)
	}
	bound, err = gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{})
	if err != nil || bound.Run.WorkflowSnapshot.SandboxProfileID != repo.DefaultSandboxProfileID {
		t.Fatalf("native preference on an unsupported host = %+v, %v", bound.Run.WorkflowSnapshot, err)
	}
}

func TestConversationWorkspaceReusesDefaultSandboxAliases(t *testing.T) {
	h, _, s := chatSandboxFixture(t)
	gateway := agentToolGateway{deps: h.deps}
	s.SandboxProfileID = "GENERIC-DOCKER"
	open := func(choice workspaceOpenChoice) conversationWorkspaceBinding {
		t.Helper()
		bound, err := gateway.bindConversationWorkspace(t.Context(), s, choice)
		if err != nil {
			t.Fatal(err)
		}
		return bound
	}
	first := open(workspaceOpenChoice{})
	if !first.Created || first.Run.WorkflowSnapshot.SandboxProfileID != repo.DefaultSandboxProfileID {
		t.Fatalf("initial workspace = %+v", first)
	}
	if next := open(workspaceOpenChoice{}); !next.Reused || next.Run.ID != first.Run.ID {
		t.Fatalf("default alias opened another workspace: %+v", next)
	}
	s.workspaceRunID = ""
	if next := open(workspaceOpenChoice{}); next.Created || next.Run.ID != first.Run.ID {
		t.Fatalf("default alias did not find the previous workspace: %+v", next)
	}
	if next := open(workspaceOpenChoice{SandboxID: s.SandboxProfileID, WorkspaceRunID: first.Run.ID}); !next.Reused || next.Run.ID != first.Run.ID {
		t.Fatalf("explicit alias did not rejoin the workspace: %+v", next)
	}
}

func TestConversationSandboxHTTPValidatesScopeAndLocks(t *testing.T) {
	h, user, s := chatSandboxFixture(t)
	selectEnvironment := func(id string, actor domain.User) int {
		payload, _ := json.Marshal(map[string]string{"sandboxProfileId": id})
		c, rec := workbenchContext(actor, "PUT", "/", bytes.NewBuffer(payload))
		c.Params = gin.Params{{Key: "id", Value: s.ChatSessionID}}
		h.sessionSandbox(c)
		return rec.Code
	}
	for id, want := range map[string]int{"private": 400, "local": 400, "missing": 400, "business": 200, "shared": 200, "": 200} {
		if got := selectEnvironment(id, user); got != want {
			t.Errorf("%s: got %d want %d", id, got, want)
		}
	}
	if code := selectEnvironment("GENERIC-DOCKER", user); code != 200 {
		t.Fatalf("default alias: %d", code)
	}
	if saved, _ := h.repo.AssistantSession(user.ID, s.ChatSessionID); saved.SandboxProfileID != repo.DefaultSandboxProfileID {
		t.Fatalf("saved default alias remains noncanonical: %q", saved.SandboxProfileID)
	}
	other := user
	other.ID += 100
	if got := selectEnvironment("business", other); got != 403 {
		t.Fatalf("other owner: %d", got)
	}
	if got := selectEnvironment("business", user); got != 200 {
		t.Fatal(got)
	}
	if err := h.repo.PutUser(domain.User{LoginName: "sandbox-peer"}); err != nil {
		t.Fatal(err)
	}
	peer, _ := h.repo.UserByLoginName("sandbox-peer")
	project, _ := h.repo.Project(s.ProjectID)
	project.Members = append(project.Members, domain.ProjectMember{UserID: peer.ID, Role: "viewer"})
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if got := selectEnvironment("shared", peer); got != 200 {
		t.Fatalf("project participant cannot select shared conversation environment: %d", got)
	}
	if stored, _ := h.repo.AssistantSession(user.ID, s.ChatSessionID); stored.SandboxProfileID != "shared" || stored.UserID != user.ID {
		t.Fatalf("participant changed conversation ownership or lost environment: %+v", stored)
	}
	if got := selectEnvironment("business", peer); got != 200 {
		t.Fatalf("project participant cannot restore environment: %d", got)
	}
	// Generic transcript updates cannot forge a workspace or overwrite this preference.
	saved, _ := h.repo.AssistantSession(user.ID, s.ChatSessionID)
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: s.ChatSessionID, ProjectID: s.ProjectID, Title: "rename"}); err != nil {
		t.Fatal(err)
	}
	after, _ := h.repo.AssistantSession(user.ID, s.ChatSessionID)
	if after.SandboxProfileID != "business" {
		t.Fatal("normal persistence lost selected sandbox")
	}
	g := domain.AssistantGeneration{ID: "sandbox-generation", SessionID: s.ChatSessionID, UserID: user.ID, Status: "running", StartedAt: time.Now()}
	if err := h.repo.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if got := selectEnvironment("shared", user); got != 409 {
		t.Fatalf("changed during generation: %d", got)
	}
	after, _ = h.repo.AssistantSession(user.ID, s.ChatSessionID)
	if after.SandboxProfileID != saved.SandboxProfileID {
		t.Fatal("rejected selection changed preference")
	}
}

func TestHistoricalGroupIsOrdinaryTaskConversationWithoutSOPControl(t *testing.T) {
	for _, op := range application.PublicOperations() {
		if op.Name == "task_groups_start" || strings.HasPrefix(op.Name, "runs_group_") {
			t.Fatalf("retired operation exposed: %s", op.Name)
		}
	}
	h, user, _ := chatSandboxFixture(t)
	run := domain.Run{ID: "old-group", Status: "cancelled", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group"}, Nodes: map[string]*domain.NodeRun{}}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	c, _ := workbenchContext(user, "POST", "/", bytes.NewBufferString(`{}`))
	req := assistantRequest{ProjectID: "workbench", RunID: run.ID, Messages: []assistantMessage{{Role: "user", Content: "what happened?"}}}
	if err := h.prepareWorkbenchContext(c, &req, user); err != nil {
		t.Fatalf("historical group conversation is not an ordinary task conversation: %v", err)
	}
	runs := &runsHandlers{deps: h.deps}
	c, rec := workbenchContext(user, "POST", "/", bytes.NewBuffer(nil))
	if !runs.rejectNonSOPControl(c, run.ID) || rec.Code != 409 {
		t.Fatalf("historical group accepted SOP control: %d", rec.Code)
	}
}

func TestStoredTriggerMessagesMayOnlyBeResentUnchanged(t *testing.T) {
	h, user := workbenchFixture(t)
	stored := []domain.AssistantMessage{{Role: "user", Content: "hi"}, {Role: "user", Trigger: "group_followup", TriggerID: "f1", Content: "续接"}}
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: "trigger-chat", ProjectID: "workbench", Messages: stored}); err != nil {
		t.Fatal(err)
	}
	saved, _ := h.repo.AssistantSession(user.ID, "trigger-chat")
	history := append([]assistantMessage{}, saved.Messages...)
	req := assistantRequest{SessionID: "trigger-chat", Messages: append(history, assistantMessage{Role: "user", Content: "continue"})}
	if err := h.validateAssistantTriggerHistory(user.ID, req); err != nil {
		t.Fatalf("unchanged history rejected: %v", err)
	}
	altered := append([]assistantMessage{}, req.Messages...)
	altered[1].Content = "改写"
	if err := h.validateAssistantTriggerHistory(user.ID, assistantRequest{SessionID: "trigger-chat", Messages: altered}); err == nil {
		t.Fatal("rewritten trigger message accepted")
	}
	forged := append([]assistantMessage{}, history...)
	forged = append(forged, assistantMessage{Role: "user", Trigger: "group_followup", Content: "伪造"})
	if err := h.validateAssistantTriggerHistory(user.ID, assistantRequest{SessionID: "trigger-chat", Messages: forged}); err == nil {
		t.Fatal("new trigger message accepted")
	}
}

// A conversation workspace is chosen by environment only: the same (or no)
// environment keeps the bound workspace, another one switches to the
// conversation's own workspace there or opens a new empty one.
func TestMainBotSwitchesConversationWorkspaces(t *testing.T) {
	h, user, s := chatSandboxFixture(t)
	gateway := agentToolGateway{deps: h.deps}
	bind := func(choice workspaceOpenChoice) conversationWorkspaceBinding {
		t.Helper()
		bound, err := gateway.bindConversationWorkspace(context.Background(), s, choice)
		if err != nil {
			t.Fatal(err)
		}
		return bound
	}
	stored := func() domain.AssistantSession {
		conv, _ := h.repo.AssistantSession(user.ID, "sandbox-chat")
		return conv
	}

	// Nothing bound yet: the first open creates the conversation's own empty
	// workspace; no repository is checked out for it.
	first := bind(workspaceOpenChoice{SandboxID: "business"})
	if !first.Created || !first.Run.IsWorkspace() || first.Previous != "" || len(first.Run.RepositoryContexts()) != 0 || stored().WorkspaceRunID != first.Run.ID || strings.Join(stored().OwnWorkspaceRunIDs, ",") != first.Run.ID {
		t.Fatalf("first workspace = %+v, stored %+v", first, stored())
	}
	if again := bind(workspaceOpenChoice{}); !again.Reused || again.Run.ID != first.Run.ID {
		t.Fatalf("open without an environment left the bound workspace: %+v", again)
	}
	if again := bind(workspaceOpenChoice{SandboxID: "business"}); !again.Reused || again.Run.ID != first.Run.ID {
		t.Fatalf("open in the same environment left the bound workspace: %+v", again)
	}

	// Another environment opens a new workspace and releases the one it leaves.
	second := bind(workspaceOpenChoice{SandboxID: "shared"})
	if !second.Created || second.Run.ID == first.Run.ID || second.Previous != first.Run.ID || second.Run.WorkflowSnapshot.SandboxProfileID != "shared" {
		t.Fatalf("second workspace = %+v", second)
	}
	if meta, ok := h.repo.WorkspaceCheckpoint(first.Run.ID); !ok || !meta.Released() {
		t.Fatalf("left workspace not released: %+v %v", meta, ok)
	}
	// A later turn starts in the workspace the conversation was left on.
	s.workspaceRunID = ""
	loadTurnWorkspace(h.deps, s, stored())
	if s.workspaceRun() != second.Run.ID {
		t.Fatalf("next turn workspace = %q", s.workspaceRun())
	}

	// Coming back to the first environment resumes its workspace.
	back := bind(workspaceOpenChoice{SandboxID: "business"})
	if back.Run.ID != first.Run.ID || back.Created || back.Reused || back.Previous != second.Run.ID {
		t.Fatalf("switch back = %+v", back)
	}
	if conv := stored(); conv.WorkspaceRunID != first.Run.ID || len(conv.OwnWorkspaceRunIDs) != 2 {
		t.Fatalf("switch back not saved: %+v", conv)
	}
	if err := h.repo.TouchWorkspaceUse(first.Run.ID, time.Now().UTC().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if meta, _ := h.repo.WorkspaceCheckpoint(first.Run.ID); meta.Released() {
		t.Fatal("a workspace used again stays released")
	}

	// Another conversation never resumes this conversation's workspaces.
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: "other-chat", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	other := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: user.ID, User: user, ChatSessionID: "other-chat", ChatOwnerID: user.ID}
	if bound, err := gateway.bindConversationWorkspace(context.Background(), other, workspaceOpenChoice{SandboxID: "business"}); err != nil || !bound.Created || bound.Run.ID == first.Run.ID {
		t.Fatalf("another conversation reused this workspace: %+v %v", bound, err)
	}
	for _, args := range []map[string]any{{"goal": "x"}, {"issueId": "chat-issue"}, {"new": true}, {"repositories": []any{"web"}}, {"sandboxProfileId": 3}} {
		if _, err := parseWorkspaceOpenChoice(args); err == nil {
			t.Fatalf("open accepted %v", args)
		}
	}
	if choice, err := parseWorkspaceOpenChoice(map[string]any{"sandboxProfileId": " shared "}); err != nil || choice.SandboxID != "shared" {
		t.Fatalf("choice = %+v %v", choice, err)
	}
}

func TestTurnContextOffersWorkspacesAndLinksIssue(t *testing.T) {
	h, user, s := chatSandboxFixture(t)
	gateway := agentToolGateway{deps: h.deps}
	now := time.Now().UTC()
	for _, spec := range []struct {
		issue, run, status string
		age                time.Duration
	}{
		{"idle-issue", "", "", 0},
		{"old-running", "old-running-sop", "running", 2 * time.Hour},
		{"new-done", "new-done-sop", "succeeded", time.Hour},
	} {
		if err := h.repo.PutIssue(domain.Issue{ID: spec.issue, ProjectID: "workbench", Title: spec.issue}); err != nil {
			t.Fatal(err)
		}
		if spec.run == "" {
			continue
		}
		run := domain.Run{ID: spec.run, Status: spec.status, CreatedAt: now.Add(-spec.age), ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", IssueID: spec.issue}, Nodes: map[string]*domain.NodeRun{}}
		if err := h.repo.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	refs := func(ids ...string) []domain.AssistantReference {
		out := []domain.AssistantReference{}
		for _, id := range ids {
			out = append(out, domain.AssistantReference{Type: "issue", ID: id})
		}
		return out
	}
	req := &assistantRequest{ProjectID: "workbench", SessionID: "sandbox-chat", Messages: []assistantMessage{{Role: "user", References: refs("idle-issue", "old-running", "new-done")}}}
	if note := h.linkMentionedIssue(req, user); !strings.Contains(note, "new-done") {
		t.Fatalf("link note = %q", note)
	}
	if stored, _ := h.repo.AssistantSession(user.ID, "sandbox-chat"); stored.IssueID != "new-done" {
		t.Fatalf("linked issue = %q", stored.IssueID)
	}
	if note := h.linkMentionedIssue(req, user); note != "" {
		t.Fatalf("a linked conversation was linked again: %q", note)
	}

	note := h.workspaceTurnContext(req, user)
	for _, want := range []string{"idle-issue「idle-issue」：没跑过 SOP", "old-running-sop（running）", "new-done-sop（succeeded）", "sandboxProfileId", "git clone"} {
		if !strings.Contains(note, want) {
			t.Fatalf("turn context lacks %q: %s", want, note)
		}
	}
	if strings.Contains(note, "当前绑定") || strings.Contains(note, "issueId") {
		t.Fatalf("an unbound conversation reported a workspace: %s", note)
	}
	own, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{SandboxID: "business"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{SandboxID: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	note = h.workspaceTurnContext(&assistantRequest{ProjectID: "workbench", SessionID: "sandbox-chat", Messages: []assistantMessage{{Role: "user"}}}, user)
	for _, want := range []string{"当前绑定的工作区：对话工作区 " + second.Run.ID + "（运行环境 shared", own.Run.ID + "（运行环境 business", "new-done-sop"} {
		if !strings.Contains(note, want) {
			t.Fatalf("turn context lacks %q: %s", want, note)
		}
	}
}

func TestDispatchFromConversationLinksIssue(t *testing.T) {
	h, user, _ := chatSandboxFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "dispatch-peer"}); err != nil {
		t.Fatal(err)
	}
	peer, _ := h.repo.UserByLoginName("dispatch-peer")
	project, _ := h.repo.Project("workbench")
	project.Members = append(project.Members, domain.ProjectMember{UserID: peer.ID, Role: "viewer"})
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "dispatched", ProjectID: "workbench", Title: "x"}
	request := func(header string) *http.Request {
		r := httptest.NewRequest("POST", "/api/issues/dispatched/dispatch", nil)
		r.Header.Set(chatSessionHeader, header)
		return r
	}
	linkDispatchingConversation(h.deps, request("sandbox-chat"), peer.ID, domain.Issue{ID: "other-project-issue", ProjectID: "other-project"})
	if stored, _ := h.repo.AssistantSession(user.ID, "sandbox-chat"); stored.IssueID != "" {
		t.Fatal("another project's dispatch linked the conversation")
	}
	linkDispatchingConversation(h.deps, request("sandbox-chat"), peer.ID, issue)
	if stored, _ := h.repo.AssistantSession(user.ID, "sandbox-chat"); stored.IssueID != "dispatched" {
		t.Fatalf("dispatching conversation not linked: %q", stored.IssueID)
	}
	linkDispatchingConversation(h.deps, request("sandbox-chat"), user.ID, domain.Issue{ID: "later", ProjectID: "workbench"})
	if stored, _ := h.repo.AssistantSession(user.ID, "sandbox-chat"); stored.IssueID != "dispatched" {
		t.Fatalf("a later dispatch replaced the linked issue: %q", stored.IssueID)
	}
}

func TestOpeningSOPWorkspaceOnlyHandsOffWhenStopped(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			h, _, s := chatSandboxFixture(t)
			h.engine = nil // Exercise the binding/response, without materializing files.
			h.deps.api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"items":[]}`))
			})
			run := domain.Run{ID: "sop-handoff", Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: s.ProjectID}}
			if stopped {
				run.Status, run.PauseRequested = "waiting", true
			}
			if err := h.repo.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if stopped {
				if _, err := h.repo.RequestRunPause(run.ID); err != nil {
					t.Fatal(err)
				}
			}
			s.workspaceRunID = run.ID
			g := agentToolGateway{deps: h.deps}
			out, err := g.openConversationWorkspace(context.Background(), s, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			var result struct{ Handoff bool }
			if err := json.Unmarshal([]byte(out), &result); err != nil || result.Handoff != stopped {
				t.Fatalf("SOP handoff = %s %v", out, err)
			}
			if (s.requestedMode() == domain.AssistantModeWorkspace) != stopped || strings.Contains(out, assistantHandoffNotice) != stopped {
				t.Fatalf("SOP handoff notice and pending mode disagree: %s", out)
			}
		})
	}
}

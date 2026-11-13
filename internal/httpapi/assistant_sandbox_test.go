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
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
)

func chatSandboxFixture(t *testing.T) (*assistantHandlers, domain.User, *AgentToolSession) {
	t.Helper()
	h, user := workbenchFixture(t)
	h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	if err := h.repo.PutDockerfile(domain.DockerfileDefinition{ID: "chat-df", Name: "Business image", ProjectID: "workbench", Content: "FROM alpine:3.20\n"}); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []domain.SandboxProfile{
		{ID: "business", Name: "Business", ProjectID: "workbench", Driver: "docker", DockerfileID: "chat-df"},
		{ID: "shared", Name: "Shared", ProjectID: "other", Shared: true, Driver: "docker", DockerfileID: "chat-df"},
		{ID: "private", Name: "Private", ProjectID: "other", Driver: "docker", DockerfileID: "chat-df"},
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
		bound, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "fix code", SandboxID: sandbox})
		return bound.Run, bound.Reused, err
	}
	if _, _, err := open("private"); err == nil || len(h.repo.Runs()) != 0 {
		t.Fatalf("another project's private sandbox accepted: %v", err)
	}
	description := gateway.workspaceOpenDescriptorFor(s).Description
	if !strings.Contains(description, "business：Business") || !strings.Contains(description, "shared：Shared") || strings.Contains(description, "private") || !strings.Contains(description, "- web") {
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
	df, _ := h.repo.Dockerfile("chat-df")
	df.Content = "FROM busybox:1.36\n"
	_ = h.repo.PutDockerfile(df)
	again, reused, err := open("shared")
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

func TestSelectConversationRepositories(t *testing.T) {
	project := domain.Project{ID: "p", Name: "P", DefaultRepositoryID: "r2"}
	all := []domain.Repository{
		{ID: "r1", ProjectID: "p", Name: "web", GongfengProjectID: "team/web"},
		{ID: "r2", ProjectID: "p", Name: "api", GongfengProjectID: "team/api-server"},
		{ID: "other", ProjectID: "q", Name: "web"},
	}
	if _, err := selectConversationRepositories(domain.Project{ID: "none"}, all, nil, false); err == nil || !strings.Contains(err.Error(), "还没有配置代码仓库") {
		t.Fatalf("no repository: %v", err)
	}
	ids := func(rcs []domain.RepositoryContext) string {
		out := []string{}
		for _, rc := range rcs {
			out = append(out, rc.RepositoryID)
		}
		return strings.Join(out, ",")
	}
	if got, err := selectConversationRepositories(project, all, nil, false); err != nil || ids(got) != "r2,r1" {
		t.Fatalf("all repositories, default first: %s %v", ids(got), err)
	}
	for want, id := range map[string]string{"WEB": "r1", "r2": "r2", "team/api-server": "r2", "api-server": "r2"} {
		if got, err := selectConversationRepositories(project, all, []string{want}, false); err != nil || ids(got) != id {
			t.Fatalf("%q => %s %v", want, ids(got), err)
		}
	}
	if _, err := selectConversationRepositories(project, all, []string{"web", "missing"}, false); err == nil || !strings.Contains(err.Error(), "可选：web、api") {
		t.Fatalf("unknown repository: %v", err)
	}
	run := domain.Run{Repositories: []domain.RepositoryContext{{RepositoryID: "r1", Name: "web"}}}
	if got := missingRepositories(run, []string{"web", "api"}); len(got) != 1 || got[0] != "api" {
		t.Fatalf("missing = %v", got)
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

func TestMainBotSwitchesConversationWorkspaces(t *testing.T) {
	h, user, s := chatSandboxFixture(t)
	gateway := agentToolGateway{deps: h.deps}
	for _, issue := range []domain.Issue{{ID: "chat-issue", ProjectID: "workbench", Title: "改首页"}, {ID: "idle-issue", ProjectID: "workbench", Title: "新需求"}} {
		if err := h.repo.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	web := domain.RepositoryContext{RepositoryID: "chat-repo", Name: "web", BaseBranch: "main", WorkBranch: "feature/issue-web"}
	sop := domain.Run{ID: "issue-sop", Status: "succeeded", CreatedAt: time.Now().UTC(), Repository: web, Repositories: []domain.RepositoryContext{web}, ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", IssueID: "chat-issue"}, Nodes: map[string]*domain.NodeRun{}}
	older := sop
	older.ID, older.CreatedAt = "issue-sop-old", sop.CreatedAt.Add(-time.Hour)
	for _, run := range []domain.Run{older, sop} {
		if err := h.repo.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
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

	// Nothing bound yet: the first open creates the conversation's own workspace.
	first := bind(workspaceOpenChoice{Goal: "fix", SandboxID: "business"})
	if !first.Created || !first.Run.IsWorkspace() || first.Previous != "" || stored().WorkspaceRunID != first.Run.ID || strings.Join(stored().OwnWorkspaceRunIDs, ",") != first.Run.ID {
		t.Fatalf("first workspace = %+v, stored %+v", first, stored())
	}
	if again := bind(workspaceOpenChoice{Goal: "fix more"}); !again.Reused || again.Run.ID != first.Run.ID {
		t.Fatalf("open without a target left the bound workspace: %+v", again)
	}

	// Switching to an issue that ran binds its newest SOP run, even after it
	// succeeded, and releases the workspace it leaves.
	onIssue := bind(workspaceOpenChoice{Goal: "改首页", IssueID: "chat-issue"})
	if onIssue.Run.ID != sop.ID || onIssue.Previous != first.Run.ID || onIssue.Created || len(h.repo.Runs()) != 3 {
		t.Fatalf("issue switch = %+v", onIssue)
	}
	if meta, ok := h.repo.WorkspaceCheckpoint(first.Run.ID); !ok || !meta.Released() {
		t.Fatalf("left workspace not released: %+v %v", meta, ok)
	}
	if conv := stored(); conv.WorkspaceRunID != sop.ID || len(conv.OwnWorkspaceRunIDs) != 1 {
		t.Fatalf("issue binding not saved: %+v", conv)
	}
	if _, err := gateway.workspaceSubmitMR(context.Background(), s, map[string]any{"title": "fix"}); err == nil || !strings.Contains(err.Error(), "Git Token") {
		t.Fatalf("a finished SOP workspace should reach delivery: %v", err)
	}
	// A later turn starts in the workspace the conversation was left on.
	s.workspaceRunID = ""
	loadTurnWorkspace(h.deps, s, stored())
	if s.workspaceRun() != sop.ID {
		t.Fatalf("next turn workspace = %q", s.workspaceRun())
	}

	if _, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "x", IssueID: "idle-issue"}); err == nil || !strings.Contains(err.Error(), "还没跑过 SOP") {
		t.Fatalf("an issue that never ran lent a workspace: %v", err)
	}

	// A different requirement may get a workspace of its own environment, and
	// the conversation can switch back to the first one.
	second := bind(workspaceOpenChoice{Goal: "新需求", New: true, SandboxID: "shared"})
	if !second.Created || second.Run.ID == first.Run.ID || second.Previous != sop.ID || second.Run.WorkflowSnapshot.SandboxProfileID != "shared" {
		t.Fatalf("second workspace = %+v", second)
	}
	back := bind(workspaceOpenChoice{Goal: "回到首页", WorkspaceRunID: first.Run.ID})
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

	// Another conversation's workspace cannot be bound by its id.
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: "other-chat", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	other := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: user.ID, User: user, ChatSessionID: "other-chat", ChatOwnerID: user.ID}
	if _, err := gateway.bindConversationWorkspace(context.Background(), other, workspaceOpenChoice{Goal: "x", WorkspaceRunID: first.Run.ID}); err == nil {
		t.Fatal("bound another conversation's workspace")
	}
	if _, err := parseWorkspaceOpenChoice(map[string]any{"goal": "x", "issueId": "chat-issue", "new": true}); err == nil {
		t.Fatal("two workspace targets accepted")
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
	for _, want := range []string{"idle-issue「idle-issue」：没跑过 SOP", "old-running-sop（running）", "new-done-sop（succeeded）", "issueId", "new"} {
		if !strings.Contains(note, want) {
			t.Fatalf("turn context lacks %q: %s", want, note)
		}
	}
	if strings.Contains(note, "当前绑定") {
		t.Fatalf("an unbound conversation reported a workspace: %s", note)
	}
	own, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "改按钮"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "看运行", IssueID: "old-running"}); err != nil {
		t.Fatal(err)
	}
	note = h.workspaceTurnContext(&assistantRequest{ProjectID: "workbench", SessionID: "sandbox-chat", Messages: []assistantMessage{{Role: "user"}}}, user)
	for _, want := range []string{"当前绑定的工作区：需求 old-running 的 SOP 运行 old-running-sop", "workbench_workspace_sop", own.Run.ID + "「改按钮」", "new-done-sop"} {
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

func TestMainBotPausesAndTakesOverIssueSOP(t *testing.T) {
	h, _, s := chatSandboxFixture(t)
	calls := []string{}
	h.deps.api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/pause") {
			run, _ := h.repo.RunState("borrowed-sop")
			run.Status = "waiting"
			_ = h.repo.PutRun(run)
			_, _ = h.repo.RequestRunPause(run.ID)
		}
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			run, _ := h.repo.RunState("borrowed-sop")
			run.Status = "cancelled"
			_ = h.repo.PutRun(run)
		}
		_, _ = w.Write([]byte(`{}`))
	})
	gateway := agentToolGateway{deps: h.deps}
	web := domain.RepositoryContext{RepositoryID: "chat-repo", Name: "web", BaseBranch: "main", WorkBranch: "feature/sop-web"}
	sop := domain.Run{ID: "borrowed-sop", Status: "running", Repository: web, Repositories: []domain.RepositoryContext{web}, ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, Nodes: map[string]*domain.NodeRun{}}
	if err := h.repo.PutRun(sop); err != nil {
		t.Fatal(err)
	}
	s.workspaceRunID = sop.ID
	ctx := context.Background()

	if _, err := gateway.workspaceSubmitMR(ctx, s, map[string]any{"title": "fix"}); err == nil || !strings.Contains(err.Error(), "takeover") {
		t.Fatalf("MR submitted from a running SOP workspace: %v", err)
	}
	if _, err := gateway.callMainBotWorkspace(ctx, s, "workbench_workspace_sop", map[string]any{"action": "resume"}); err == nil {
		t.Fatal("resumed an SOP that was not paused")
	}
	if out, err := gateway.callMainBotWorkspace(ctx, s, "workbench_workspace_sop", map[string]any{"action": "pause"}); err != nil || !strings.Contains(out, `"handoff":true`) || !strings.Contains(out, assistantHandoffNotice) || s.requestedMode() != domain.AssistantModeWorkspace {
		t.Fatalf("pause must hand off after the SOP stops: %s %v", out, err)
	}
	out, err := gateway.callMainBotWorkspace(ctx, s, "workbench_workspace_sop", map[string]any{"action": "takeover"})
	if err != nil || !strings.Contains(out, "已结束") || !strings.Contains(out, `"handoff":true`) || !strings.Contains(out, assistantHandoffNotice) {
		t.Fatalf("takeover = %s %v", out, err)
	}
	if want := []string{"POST /api/runs/borrowed-sop/pause", "POST /api/runs/borrowed-sop/cancel"}; strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("run control calls = %v", calls)
	}
	if bound, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "接着改"}); err != nil || !bound.Reused || bound.Run.ID != sop.ID || len(h.repo.Runs()) != 1 {
		t.Fatalf("taken-over SOP workspace not kept: %+v %v", bound, err)
	}
	if _, err := gateway.workspaceSubmitMR(ctx, s, map[string]any{"title": "fix"}); err == nil || !strings.Contains(err.Error(), "Git Token") {
		t.Fatalf("taken-over workspace should reach delivery: %v", err)
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
			out, err := g.openConversationWorkspace(context.Background(), s, map[string]any{"goal": "查看当前 SOP"})
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

func TestWorkspaceSOPResumeHandsOffBeforeResuming(t *testing.T) {
	h, _, s := chatSandboxFixture(t)
	called := false
	h.deps.api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{}`))
	})
	run := domain.Run{ID: "resume-after-exit", Status: "waiting", PauseRequested: true, ExecutionContext: domain.TelemetryContext{ProjectID: s.ProjectID}}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	s.RuntimeMode, s.workspaceRunID = domain.AssistantModeWorkspace, run.ID
	g := agentToolGateway{deps: h.deps}
	out, err := g.controlConversationSOP(context.Background(), s, map[string]any{"action": "resume", "context": "已修改"})
	if err != nil || called || !strings.Contains(out, `"handoff":true`) || !strings.Contains(out, assistantHandoffNotice) || s.requestedMode() != domain.AssistantModePlatform || s.modeAction == nil {
		t.Fatalf("SOP resumed before CLI exit or omitted handoff: %s %v", out, err)
	}
	if err := s.modeAction(context.Background()); err != nil || !called {
		t.Fatalf("deferred SOP resume failed: %v", err)
	}
}

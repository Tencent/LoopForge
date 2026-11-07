package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func conversationIssueFixture(t *testing.T) (*agentToolGateway, *AgentToolSession, *repo.Store) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("alice")
	if err := st.PutProject(domain.Project{ID: "p", Name: "P", Key: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAssistantSession(user.ID, domain.AssistantSession{ID: "chat-1", ProjectID: "p", Title: "修登录页"}); err != nil {
		t.Fatal(err)
	}
	ws := domain.Run{ID: "ws-1", Kind: domain.RunKindWorkspace, WorkflowID: domain.ConversationWorkspaceWorkflowID, Status: "waiting", Input: "对话改代码：修登录页文案",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID}, ExecutionContext: domain.TelemetryContext{ProjectID: "p", ResponsibleUserID: user.ID},
		Repositories: []domain.RepositoryContext{{RepositoryID: "repo-web", Name: "web"}}, Nodes: map[string]*domain.NodeRun{}}
	sop := domain.Run{ID: "sop-1", WorkflowID: "wf", Status: "succeeded", WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf"}, ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, Nodes: map[string]*domain.NodeRun{}}
	for _, run := range []domain.Run{ws, sop} {
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", UserID: user.ID, User: user, ChatSessionID: "chat-1", ChatOwnerID: user.ID}
	return &agentToolGateway{deps: &deps{repo: st}}, s, st
}

func TestParseConversationRequirement(t *testing.T) {
	if req, err := parseConversationRequirement(map[string]any{}); err != nil || req.Title != "" {
		t.Fatalf("absent requirement = %+v %v", req, err)
	}
	if req, err := parseConversationRequirement(map[string]any{"requirement": map[string]any{"title": "  修复登录 ", "description": " 背景 "}}); err != nil || req.Title != "修复登录" || req.Description != "背景" {
		t.Fatalf("requirement = %+v %v", req, err)
	}
	for name, raw := range map[string]any{
		"not object": "修复",
		"no title":   map[string]any{"description": "x"},
		"long title": map[string]any{"title": strings.Repeat("长", 201)},
		"long body":  map[string]any{"title": "t", "description": strings.Repeat("x", 20001)},
	} {
		if _, err := parseConversationRequirement(map[string]any{"requirement": raw}); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if choice, err := parseWorkspaceOpenChoice(map[string]any{"goal": "g", "requirement": map[string]any{"title": "需求"}}); err != nil || choice.Requirement.Title != "需求" {
		t.Fatalf("open choice = %+v %v", choice, err)
	}
	if _, err := parseWorkspaceOpenChoice(map[string]any{"goal": "g", "requirement": map[string]any{}}); err == nil {
		t.Fatal("open must reject a requirement without title")
	}
	var schema map[string]any
	if err := json.Unmarshal(workspaceOpenDescriptor().InputSchema, &schema); err != nil || !strings.Contains(string(workspaceOpenDescriptor().InputSchema), `"requirement"`) {
		t.Fatalf("open schema must offer requirement: %v", err)
	}
}

// A conversation that changes code identifies its requirement: it lands in
// the requirement list, is linked to the conversation and the workspace, and
// the MRs it submits become the requirement's delivery MRs.
func TestConversationRequirementCreatedAndLinked(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	run, _ := st.RunState("ws-1")
	if issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{}, false); err != nil || created || issue.ID != "" {
		t.Fatalf("without a requirement nothing may be created: %+v %v %v", issue, created, err)
	}
	issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "修复登录页文案", Description: "按钮文案错了"}, false)
	if err != nil || !created || issue.ID == "" {
		t.Fatalf("create = %+v %v %v", issue, created, err)
	}
	if issue.ProjectID != "p" || issue.Kind != "requirement" || issue.Source != "local" || issue.Status != "in_progress" || issue.AutomationState != "ignored" || issue.AssigneeUserID != s.UserID || issue.ConversationSessionID != "chat-1" || issue.RepositoryID != "repo-web" || issue.Number == 0 {
		t.Fatalf("created requirement = %+v", issue)
	}
	if len(issue.Operations) != 1 || !strings.Contains(issue.Operations[0].Detail, "修登录页") || issue.Operations[0].Actor != "爱丽丝" {
		t.Fatalf("operations = %+v", issue.Operations)
	}
	if got, _ := st.RunState("ws-1"); got.ExecutionContext.IssueID != issue.ID {
		t.Fatalf("workspace not linked: %q", got.ExecutionContext.IssueID)
	}
	if conv, _ := st.AssistantSession(s.UserID, "chat-1"); conv.IssueID != issue.ID {
		t.Fatalf("conversation not linked: %q", conv.IssueID)
	}
	// A linked workspace keeps its requirement.
	again, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "另一个"}, false)
	if err != nil || created || again.ID != issue.ID {
		t.Fatalf("relink = %+v %v %v", again, created, err)
	}

	results := []workspaceMRResult{
		{Repository: "web", MRURL: "https://git.example.com/team/web/merge_requests/7", TargetBranch: "master"},
		{Repository: "api", Error: "push failed"},
	}
	if err := h.recordConversationMRs(issue.ID, results, run.RepositoryContexts()); err != nil {
		t.Fatal(err)
	}
	if err := h.recordConversationMRs(issue.ID, results, run.RepositoryContexts()); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.Issue(issue.ID)
	mrs := stored.DeliveryMRList()
	if len(mrs) != 1 || mrs[0].IID != 7 || mrs[0].Project != "team/web" || mrs[0].RepositoryID != "repo-web" || stored.Status != "in_review" {
		t.Fatalf("delivery MRs = %+v status=%s", mrs, stored.Status)
	}
	states := st.MRDeliveryStatesMap([]domain.MRDeliveryKey{{Project: "team/web", IID: 7}})
	if states[domain.MRDeliveryKey{Project: "team/web", IID: 7}].IssueID != issue.ID {
		t.Fatalf("MR not registered for polling: %+v", states)
	}
	// Once the MR merges, polling completes the requirement like an SOP delivery.
	if err := st.UpdateMRDeliveryState("team/web", 7, "merged", "master", time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if done, _ := st.Issue(issue.ID); done.Status != "done" || done.DeliveryState != "merged" {
		t.Fatalf("merged MR must deliver the requirement: status=%s delivery=%s", done.Status, done.DeliveryState)
	}
	if err := h.recordConversationMRs("", results, nil); err != nil {
		t.Fatal(err)
	}
}

// GitHub pull requests opened from a conversation workspace are delivery MRs
// too: polling tracks them under the github: project key.
func TestConversationRequirementRecordsGitHubPullRequest(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	run, _ := st.RunState("ws-1")
	issue, _, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "修复登录页文案"}, false)
	if err != nil {
		t.Fatal(err)
	}
	results := []workspaceMRResult{{Repository: "web", MRURL: "https://github.com/acme/web/pull/9", TargetBranch: "main"}}
	if err := h.recordConversationMRs(issue.ID, results, run.RepositoryContexts()); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.Issue(issue.ID)
	mrs := stored.DeliveryMRList()
	if len(mrs) != 1 || mrs[0].Project != "github:acme/web" || mrs[0].IID != 9 || mrs[0].URL != "https://github.com/acme/web/pull/9" || mrs[0].RepositoryID != "repo-web" || stored.Status != "in_review" {
		t.Fatalf("delivery PRs = %+v status=%s", mrs, stored.Status)
	}
	key := domain.MRDeliveryKey{Project: "github:acme/web", IID: 9}
	if st.MRDeliveryStatesMap([]domain.MRDeliveryKey{key})[key].IssueID != issue.ID {
		t.Fatal("pull request not registered for polling")
	}
	if err := st.UpdateMRDeliveryState("github:acme/web", 9, "merged", "main", time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if done, _ := st.Issue(issue.ID); done.Status != "done" || done.DeliveryState != "merged" {
		t.Fatalf("merged pull request must deliver the requirement: status=%s delivery=%s", done.Status, done.DeliveryState)
	}
}

// Submitting from a workspace without a requirement reuses the
// conversation's own open requirement first, else creates one from the MR.
func TestConversationRequirementPrefersSessionIssue(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	if err := st.PutIssue(domain.Issue{ID: "iss-open", ProjectID: "p", Title: "已有需求", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LinkAssistantSessionIssue(s.UserID, "chat-1", "iss-open"); err != nil {
		t.Fatal(err)
	}
	run, _ := st.RunState("ws-1")
	issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "MR 标题"}, true)
	if err != nil || created || issue.ID != "iss-open" {
		t.Fatalf("session requirement must be reused: %+v %v %v", issue, created, err)
	}
	if got, _ := st.RunState("ws-1"); got.ExecutionContext.IssueID != "iss-open" {
		t.Fatalf("workspace = %q", got.ExecutionContext.IssueID)
	}

	// A finished session requirement is not reopened by new work.
	h, s, st = conversationIssueFixture(t)
	if err := st.PutIssue(domain.Issue{ID: "iss-done", ProjectID: "p", Title: "旧需求", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LinkAssistantSessionIssue(s.UserID, "chat-1", "iss-done"); err != nil {
		t.Fatal(err)
	}
	run, _ = st.RunState("ws-1")
	issue, created, err = h.linkConversationRequirement(s, run, conversationRequirement{Title: "MR 标题"}, true)
	if err != nil || !created || issue.ID == "iss-done" || issue.Title != "MR 标题" {
		t.Fatalf("new work after a finished requirement = %+v %v %v", issue, created, err)
	}
	if conv, _ := st.AssistantSession(s.UserID, "chat-1"); conv.IssueID != "iss-done" {
		t.Fatalf("an already linked conversation keeps its link: %q", conv.IssueID)
	}

	// SOP runs are never relinked from a conversation.
	sop, _ := st.RunState("sop-1")
	if issue, created, err := h.linkConversationRequirement(s, sop, conversationRequirement{Title: "x"}, false); err != nil || created || issue.ID != "" {
		t.Fatalf("SOP run relinked: %+v %v %v", issue, created, err)
	}
	if got, _ := st.RunState("sop-1"); got.ExecutionContext.IssueID != "" {
		t.Fatalf("SOP run issue = %q", got.ExecutionContext.IssueID)
	}
	// Another project's workspace is out of scope.
	other := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "q", UserID: s.UserID, User: s.User, ChatSessionID: "chat-1", ChatOwnerID: s.UserID}
	if issue, created, err := h.linkConversationRequirement(other, run, conversationRequirement{Title: "x"}, false); err != nil || created || issue.ID != "" {
		t.Fatalf("cross-project link: %+v %v %v", issue, created, err)
	}
}

func TestConversationUsageAttributedToWorkspace(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	attributeConversationUsage(h.deps, nil, "glm-5", &domain.TokenUsage{TotalTokens: 1})
	attributeConversationUsage(h.deps, s, "glm-5", nil)
	attributeConversationUsage(h.deps, s, "glm-5", &domain.TokenUsage{TotalTokens: 5})
	if run, _ := st.RunState("ws-1"); run.Usage.TotalTokens != 0 {
		t.Fatalf("a turn without a bound workspace must not attribute usage: %+v", run.Usage)
	}
	s.workspaceRunID = "ws-1"
	attributeConversationUsage(h.deps, s, "claude-sonnet-4", &domain.TokenUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120})
	attributeConversationUsage(h.deps, s, "claude-sonnet-4", &domain.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12})
	run, _ := st.RunState("ws-1")
	if run.Usage.TotalTokens != 132 || run.Usage.InputTokens != 110 || run.Usage.OutputTokens != 22 {
		t.Fatalf("workspace usage = %+v", run.Usage)
	}
	// A shared task conversation never attributes to a conversation workspace.
	task := &AgentToolSession{RunID: "sop-1", workspaceRunID: "ws-1"}
	attributeConversationUsage(h.deps, task, "claude-sonnet-4", &domain.TokenUsage{TotalTokens: 1000})
	if again, _ := st.RunState("ws-1"); again.Usage.TotalTokens != 132 {
		t.Fatalf("task conversation leaked usage: %+v", again.Usage)
	}
}

// A new conversation is stored only when its first turn ends, after the tool
// created the requirement: the end of the turn links it.
func TestConversationFirstTurnLinksRequirementAfterSave(t *testing.T) {
	h, s, st := conversationIssueFixture(t)
	s.ChatSessionID = "chat-new"
	s.workspaceRunID = "ws-1"
	run, _ := st.RunState("ws-1")
	issue, created, err := h.linkConversationRequirement(s, run, conversationRequirement{Title: "新会话里的需求"}, false)
	if err != nil || !created || issue.ConversationSessionID != "chat-new" {
		t.Fatalf("create = %+v %v %v", issue, created, err)
	}
	if _, ok := st.AssistantSession(s.UserID, "chat-new"); ok {
		t.Fatal("fixture: the conversation must not exist before its first turn is saved")
	}
	if err := st.PutAssistantSession(s.UserID, domain.AssistantSession{ID: "chat-new", ProjectID: "p", Title: "新会话", WorkspaceRunID: "ws-1"}); err != nil {
		t.Fatal(err)
	}
	attributeConversationUsage(h.deps, s, "glm-5", nil)
	if conv, _ := st.AssistantSession(s.UserID, "chat-new"); conv.IssueID != issue.ID {
		t.Fatalf("conversation not linked after its first turn: %q", conv.IssueID)
	}
	// An already linked conversation keeps its requirement.
	if err := st.PutIssue(domain.Issue{ID: "iss-other", ProjectID: "p", Title: "另一个", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LinkConversationRunIssue("ws-1", "iss-other"); err != nil {
		t.Fatal(err)
	}
	attributeConversationUsage(h.deps, s, "glm-5", nil)
	if conv, _ := st.AssistantSession(s.UserID, "chat-new"); conv.IssueID != issue.ID {
		t.Fatalf("conversation relinked: %q", conv.IssueID)
	}
	// Another project's turn never links.
	s.ProjectID = "q"
	s.ChatSessionID = "chat-1"
	attributeConversationUsage(h.deps, s, "glm-5", nil)
	if conv, _ := st.AssistantSession(s.UserID, "chat-1"); conv.IssueID != "" {
		t.Fatalf("cross-project link: %q", conv.IssueID)
	}
}

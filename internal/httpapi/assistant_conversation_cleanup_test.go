package httpapi

import (
	"bytes"
	"net/http"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

// Deleting a conversation that worked on several requirements keeps the ones
// it handed in (an MR, whatever its state, or a direct push) together with
// their workspace records, which only end, and deletes the requirements it
// created but never handed in. Requirements from elsewhere, or dispatched to an
// SOP, are never deleted.
func TestDeletingConversationSettlesItsRequirements(t *testing.T) {
	h, owner := workbenchFixture(t)
	h.deps.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	const chat = "chat-multi"
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: chat, ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "做几个需求"}}}); err != nil {
		t.Fatal(err)
	}
	issue := func(id string, edit func(*domain.Issue)) {
		t.Helper()
		v := domain.Issue{ID: id, ProjectID: "workbench", Title: id, Kind: "requirement", Source: "local", Status: "in_progress", ConversationSessionID: chat}
		if edit != nil {
			edit(&v)
		}
		if err := h.repo.PutIssue(v); err != nil {
			t.Fatal(err)
		}
	}
	workspace := func(id, issueID string) {
		t.Helper()
		run := domain.Run{ID: id, Kind: domain.RunKindWorkspace, WorkflowID: domain.ConversationWorkspaceWorkflowID, Status: "waiting", Nodes: map[string]*domain.NodeRun{}, ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", IssueID: issueID},
			Repositories: []domain.RepositoryContext{{Name: "web", BaseBranch: "main", WorkBranch: "feature/" + id}}}
		if err := h.repo.PutRun(run); err != nil {
			t.Fatal(err)
		}
		if err := h.repo.SetAssistantSessionWorkspaceRun(owner.ID, chat, id, true); err != nil {
			t.Fatal(err)
		}
	}
	mr := func(state string) func(*domain.Issue) {
		return func(v *domain.Issue) {
			v.Status = "in_review"
			v.SetDeliveryMRs([]domain.DeliveryMR{{Repository: "web", Project: "g/web", IID: 7, URL: "https://git.example.com/g/web/merge_requests/7", State: state}})
		}
	}
	issue("issue-open", nil)
	workspace("ws-open", "issue-open")
	issue("issue-mr", mr("opened"))
	workspace("ws-mr", "issue-mr")
	issue("issue-mr-closed", func(v *domain.Issue) { mr("closed")(v); v.Status = "in_progress" })
	workspace("ws-mr-closed", "issue-mr-closed")
	issue("issue-direct", func(v *domain.Issue) { v.Status = "in_review" })
	workspace("ws-direct", "issue-direct")
	issue("issue-sop", nil)
	workspace("ws-sop", "issue-sop")
	if err := h.repo.PutTask(domain.Task{ID: "task-sop", IssueID: "issue-sop", WorkflowID: "generic-backend-delivery", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	issue("issue-tapd", func(v *domain.Issue) { v.Source = "tapd"; v.ConversationSessionID = "" })
	workspace("ws-tapd", "issue-tapd")
	workspace("ws-plain", "")
	// Created by the conversation, whose workspace is already gone.
	issue("issue-linked", nil)
	if _, err := h.repo.LinkAssistantSessionIssue(owner.ID, chat, "issue-linked"); err != nil {
		t.Fatal(err)
	}

	api := New(h.repo, h.deps.engine, tracker.NewService(), "", "")
	conversation := func(issueID string) domain.RequirementConversation {
		t.Helper()
		snap := requestJSON[domain.RequirementSnapshot](t, api, http.MethodGet, "/api/issues/"+issueID+"/requirement", nil, http.StatusOK)
		if snap.Conversation == nil {
			t.Fatalf("requirement %s has no conversation", issueID)
		}
		return *snap.Conversation
	}
	if conv := conversation("issue-mr"); conv.SessionID != chat || !conv.Available || len(conv.Branches) != 1 || conv.Branches[0].Repository != "web" || conv.Branches[0].WorkBranch != "feature/ws-mr" || conv.Branches[0].BaseBranch != "main" {
		t.Fatalf("conversation of a live chat = %+v", conv)
	}

	c, w := workbenchContext(owner, "DELETE", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: chat}}
	h.session(c)
	if w.Code != 200 {
		t.Fatalf("delete = %d %s", w.Code, w.Body.String())
	}
	if _, ok := h.repo.AssistantSession(owner.ID, chat); ok {
		t.Fatal("conversation not deleted")
	}

	for _, id := range []string{"issue-open", "issue-linked"} {
		if _, ok := h.repo.Issue(id); ok {
			t.Fatalf("requirement %s the conversation never handed in was kept", id)
		}
	}
	for _, id := range []string{"issue-mr", "issue-mr-closed", "issue-direct", "issue-sop", "issue-tapd"} {
		if _, ok := h.repo.Issue(id); !ok {
			t.Fatalf("requirement %s was deleted", id)
		}
	}
	for _, id := range []string{"ws-mr", "ws-mr-closed", "ws-direct"} {
		run, ok := h.repo.RunState(id)
		if !ok {
			t.Fatalf("workspace %s of a handed-in requirement was deleted; analytics reads it", id)
		}
		if run.Status != "cancelled" {
			t.Fatalf("workspace %s status = %s, want ended so its sandbox is reclaimed", id, run.Status)
		}
	}
	if conv := conversation("issue-mr"); conv.SessionID != chat || conv.Available || len(conv.Branches) != 1 {
		t.Fatalf("conversation of a deleted chat = %+v, want it marked deleted and its branch kept", conv)
	}
	if snap := requestJSON[domain.RequirementSnapshot](t, api, http.MethodGet, "/api/issues/issue-tapd/requirement", nil, http.StatusOK); snap.Conversation != nil {
		t.Fatalf("a requirement from elsewhere whose workspace is gone has no conversation: %+v", snap.Conversation)
	}
	for _, id := range []string{"ws-open", "ws-sop", "ws-tapd", "ws-plain"} {
		if _, ok := h.repo.RunState(id); ok {
			t.Fatalf("workspace %s was left behind", id)
		}
	}
}

// The creating conversation decides whether deleting a conversation deletes a
// requirement, so the issue API neither sets nor moves it.
func TestIssueSaveKeepsConversationSessionServerOwned(t *testing.T) {
	h, _ := workbenchFixture(t)
	h.deps.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	api := New(h.repo, h.deps.engine, tracker.NewService(), "", "")
	created := requestJSON[domain.Issue](t, api, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "workbench", "title": "手工需求", "conversationSessionId": "chat-forged",
	}, http.StatusOK)
	if stored, _ := h.repo.Issue(created.ID); stored.ConversationSessionID != "" {
		t.Fatalf("new issue conversation = %q, want none", stored.ConversationSessionID)
	}
	if err := h.repo.PutIssue(domain.Issue{ID: "issue-chat", ProjectID: "workbench", Title: "对话需求", Kind: "requirement", Source: "local", Status: "in_progress", ConversationSessionID: "chat-own"}); err != nil {
		t.Fatal(err)
	}
	for _, forged := range []string{"chat-forged", ""} {
		requestJSON[domain.Issue](t, api, http.MethodPut, "/api/issues", map[string]any{
			"id": "issue-chat", "title": "改了标题", "conversationSessionId": forged,
		}, http.StatusOK)
		if stored, _ := h.repo.Issue("issue-chat"); stored.ConversationSessionID != "chat-own" || stored.Title != "改了标题" {
			t.Fatalf("after saving %q: conversation = %q title = %q", forged, stored.ConversationSessionID, stored.Title)
		}
	}
}

func TestConversationIssueDelivered(t *testing.T) {
	for _, tc := range []struct {
		name  string
		issue domain.Issue
		want  bool
	}{
		{"in progress", domain.Issue{Status: "in_progress"}, false},
		{"cancelled", domain.Issue{Status: "cancelled"}, false},
		{"done", domain.Issue{Status: "done"}, true},
		{"direct push", domain.Issue{Status: "in_review"}, true},
		{"legacy MR", domain.Issue{Status: "in_progress", DeliveryMRURL: "https://git.example.com/g/web/merge_requests/1"}, true},
		{"merged", domain.Issue{Status: "in_progress", DeliveryState: "merged"}, true},
	} {
		if got := conversationIssueDelivered(tc.issue); got != tc.want {
			t.Errorf("%s: delivered = %v, want %v", tc.name, got, tc.want)
		}
	}
}

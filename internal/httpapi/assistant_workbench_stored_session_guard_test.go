package httpapi

import (
	"bytes"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// prepareWorkbenchContext reads the stored conversation once and reuses it for
// every check. These cases pin the two checks that depend on that read: a
// conversation saved under another project is refused, and a conversation
// bound to an issue validates and injects that issue on every turn.

func TestPrepareWorkbenchContextRefusesConversationOfAnotherProject(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutProject(domain.Project{ID: "other", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "stored-elsewhere", ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "讨论", AuthorID: u.ID}}}); err != nil {
		t.Fatal(err)
	}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))

	req := assistantRequest{SessionID: "stored-elsewhere", ProjectID: "other", Messages: []assistantMessage{{Role: "user", Content: "继续"}}}
	err := h.prepareWorkbenchContext(c, &req, u)
	if err == nil || !strings.Contains(err.Error(), "会话属于其他项目") {
		t.Fatalf("conversation of another project accepted: %v", err)
	}

	// The same conversation is fine in its own project, and an unknown
	// session ID is just a new conversation.
	req = assistantRequest{SessionID: "stored-elsewhere", ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "继续"}}}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatalf("own project conversation refused: %v", err)
	}
	req = assistantRequest{SessionID: "never-saved", ProjectID: "other", Messages: []assistantMessage{{Role: "user", Content: "新会话"}}}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatalf("new conversation refused: %v", err)
	}
}

func TestPrepareWorkbenchContextValidatesAndInjectsStoredSessionIssue(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutProject(domain.Project{ID: "other", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	putContextIssue(t, h, "bound-primary", "workbench", "已绑定的主需求")
	if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "issue-bound", ProjectID: "workbench", IssueID: "bound-primary", Messages: []domain.AssistantMessage{{Role: "user", Content: "讨论", AuthorID: u.ID}}}); err != nil {
		t.Fatal(err)
	}
	if stored, ok := h.repo.AssistantSession(u.ID, "issue-bound"); !ok || stored.IssueID != "bound-primary" {
		t.Fatalf("fixture did not keep the issue binding: %+v (found=%v)", stored, ok)
	}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))

	req := assistantRequest{SessionID: "issue-bound", ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "继续"}}}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatalf("bound conversation refused: %v", err)
	}
	if !strings.Contains(req.contextText, "当前会话关联的主需求") || !strings.Contains(req.contextText, "已绑定的主需求") {
		t.Fatalf("bound issue not injected into context:\n%s", req.contextText)
	}

	// The binding is re-validated every turn: once the issue leaves the
	// project, the conversation must stop using it.
	putContextIssue(t, h, "bound-primary", "other", "已移走的主需求")
	req = assistantRequest{SessionID: "issue-bound", ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "继续"}}}
	err := h.prepareWorkbenchContext(c, &req, u)
	if err == nil || !strings.Contains(err.Error(), "需求不存在或不属于当前项目") {
		t.Fatalf("moved issue still accepted: %v\n%s", err, req.contextText)
	}
}

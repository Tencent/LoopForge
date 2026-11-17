package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"agentflow/internal/domain"
)

// "创建并去对话澄清" opens a new conversation whose first message @-mentions
// the requirement. The browser saves the empty conversation first, so the
// turn links the mentioned issue to it while starting; that link must not be
// mistaken for a concurrent rebinding and reject the turn. The link does not
// depend on how the requirement is delivered, so every SOP choice of the
// new-requirement dialog is covered.
func TestFirstTurnMentioningAnIssueLinksItAndStarts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		issue domain.Issue
	}{
		{"bound SOP", domain.Issue{WorkflowID: "generic-backend-delivery"}},
		{"SOP chosen later", domain.Issue{}},
		{"no SOP", domain.Issue{SOPMode: domain.IssueSOPNone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, u := workbenchFixture(t)
			h.generate = func(context.Context, string, domain.User) (string, error) {
				return `{"message":"好的","drafts":[]}`, nil
			}
			issue := tc.issue
			issue.ID, issue.ProjectID, issue.Title = "clarify-issue", "workbench", "test1"
			if err := h.repo.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "clarify-chat", ProjectID: "workbench"}); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{
				"sessionId": "clarify-chat",
				"projectId": "workbench",
				"messages": []domain.AssistantMessage{{
					Role:       "user",
					Content:    "@test1 aaaa",
					References: []domain.AssistantReference{{Type: "issue", ID: "clarify-issue", Name: "test1", ProjectID: "workbench"}},
				}},
			})
			c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
			h.startGeneration(c)
			if rec.Code != 202 {
				t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
			}
			s := waitForCompletedGeneration(t, h, u.ID, "clarify-chat")
			if s.IssueID != "clarify-issue" {
				t.Fatalf("mentioned issue not linked: %q", s.IssueID)
			}
		})
	}
}

// Rebinding the conversation to another issue while a turn starts is still
// refused, so the turn never runs against a context it did not read.
func TestTurnRefusesAConcurrentIssueRebinding(t *testing.T) {
	h, u := workbenchFixture(t)
	for _, id := range []string{"first-issue", "other-issue"} {
		if err := h.repo.PutIssue(domain.Issue{ID: id, ProjectID: "workbench", Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "rebind-chat", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	stale, _ := h.repo.AssistantSession(u.ID, "rebind-chat")
	if _, err := h.repo.SetAssistantSessionIssue(u.ID, "rebind-chat", "other-issue", stale.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	g := domain.AssistantGeneration{ID: "rebind-generation", SessionID: "rebind-chat", UserID: u.ID, ProjectID: "workbench", Status: "running"}
	pending := domain.AssistantSession{ID: "rebind-chat", ProjectID: "workbench", IssueID: stale.IssueID, Messages: []domain.AssistantMessage{{Role: "user", Content: "hi"}}}
	if err := h.repo.BeginAssistantGeneration(g, pending); err == nil {
		t.Fatal("a turn started over a concurrent issue rebinding")
	}
}

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// "创建并去对话澄清" opens a new conversation whose first message @-mentions
// the requirement. The browser saves the empty conversation first, so the
// turn links the mentioned issue to it while starting; that link must not be
// mistaken for a concurrent rebinding and reject the turn. The link does not
// depend on how the requirement is delivered, so every LOOP choice of the
// new-requirement dialog is covered.
func TestFirstTurnMentioningAnIssueLinksItAndStarts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		issue domain.Issue
	}{
		{"bound LOOP", domain.Issue{WorkflowID: "generic-backend-delivery"}},
		{"LOOP chosen later", domain.Issue{}},
		{"no LOOP", domain.Issue{SOPMode: domain.IssueSOPNone}},
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

func TestIssueReferenceAndTurnContextIncludeBoundSOPAndDeliveryMode(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{
		ID:        "wf-fe",
		ProjectID: "workbench",
		Name:      "前端交付",
		Status:    "published",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{
		ID:        "wf-be",
		ProjectID: "workbench",
		Name:      "后端交付",
		Status:    "published",
	}); err != nil {
		t.Fatal(err)
	}
	for _, issue := range []domain.Issue{
		{
			ID:         "issue-single-sop",
			ProjectID:  "workbench",
			Title:      "列表导出 CSV",
			Status:     "backlog",
			WorkflowID: "wf-fe",
			BaseBranch: "release/v1",
		},
		{
			ID:        "issue-chain-sop",
			ProjectID: "workbench",
			Title:     "全栈报表",
			Status:    "backlog",
			SOPBindings: []domain.IssueSOPBinding{
				{WorkflowID: "wf-fe", Order: 1},
				{WorkflowID: "wf-be", Order: 2},
			},
		},
		{
			ID:        "issue-no-sop",
			ProjectID: "workbench",
			Title:     "直接改样式",
			Status:    "backlog",
			SOPMode:   domain.IssueSOPNone,
		},
	} {
		if err := h.repo.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "sop-context-chat", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}

	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	req := assistantRequest{
		SessionID: "sop-context-chat",
		ProjectID: "workbench",
		Messages: []assistantMessage{{
			Role:       "user",
			Content:    "@列表导出 CSV",
			References: []domain.AssistantReference{{Type: "issue", ID: "issue-single-sop"}},
		}},
	}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"workflowId":"wf-fe"`,
		`"boundSops":[{"name":"前端交付","workflowId":"wf-fe"}]`,
		`"baseBranch":"release/v1"`,
		"已绑定 LOOP：前端交付（wf-fe）",
		"不要再反问用户「直接改代码还是走 LOOP」",
	} {
		if !strings.Contains(req.contextText, want) {
			t.Fatalf("single-SOP context missing %q:\n%s", want, req.contextText)
		}
	}

	// Subsequent turn without explicit @-mention still sees the linked issue's bound SOP.
	followUp := assistantRequest{
		SessionID: "sop-context-chat",
		ProjectID: "workbench",
		Messages:  []assistantMessage{{Role: "user", Content: "补充一下导出字段"}},
	}
	if err := h.prepareWorkbenchContext(c, &followUp, u); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"boundSops":[{"name":"前端交付","workflowId":"wf-fe"}]`,
		"已绑定 LOOP：前端交付（wf-fe）",
	} {
		if !strings.Contains(followUp.contextText, want) {
			t.Fatalf("follow-up context missing %q:\n%s", want, followUp.contextText)
		}
	}

	// Multi-step SOP bindings keep chain order.
	chainReq := assistantRequest{
		ProjectID: "workbench",
		Messages: []assistantMessage{{
			Role:       "user",
			Content:    "@全栈报表",
			References: []domain.AssistantReference{{Type: "issue", ID: "issue-chain-sop"}},
		}},
	}
	if err := h.prepareWorkbenchContext(c, &chainReq, u); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chainReq.contextText, "已绑定 LOOP：前端交付（wf-fe） → 后端交付（wf-be）") {
		t.Fatalf("chain SOP context missing bound chain:\n%s", chainReq.contextText)
	}

	// Explicit no-SOP mode reports sopMode=none without boundSops.
	noSOPReq := assistantRequest{
		ProjectID: "workbench",
		Messages: []assistantMessage{{
			Role:       "user",
			Content:    "@直接改样式",
			References: []domain.AssistantReference{{Type: "issue", ID: "issue-no-sop"}},
		}},
	}
	if err := h.prepareWorkbenchContext(c, &noSOPReq, u); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(noSOPReq.contextText, `"sopMode":"none"`) || strings.Contains(noSOPReq.contextText, `"boundSops"`) || !strings.Contains(noSOPReq.contextText, "交付方式：不使用 LOOP") {
		t.Fatalf("no-SOP context unexpected:\n%s", noSOPReq.contextText)
	}
}

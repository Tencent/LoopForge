package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/tracker"
)

// The workbench runs a confirmed draft through /mcp with the chat session
// header. Models put only the fields they change into an issues_save draft, so
// confirming "switch the LOOP" must not clear the description or reset
// priority and status the way a full PUT /api/issues replacement does.
func TestConfirmedIssueSaveDraftKeepsOmittedFields(t *testing.T) {
	for _, tc := range []struct {
		name     string
		confirm  bool
		wantKept bool
	}{
		{"confirmed workbench draft", true, true},
		{"plain MCP call keeps full replacement", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user, _ := chatSandboxFixture(t)
			for _, id := range []string{"loop-a", "loop-b"} {
				if err := h.repo.PutWorkflow(domain.WorkflowDefinition{ID: id, Name: id, ProjectID: "workbench", Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "command", Command: "true"}}}); err != nil {
					t.Fatal(err)
				}
			}
			issue := domain.Issue{ID: "draft-issue", ProjectID: "workbench", Title: "human_bytes 边界", Description: "human_bytes(1024) 应为 1.0 KiB", Priority: "high", Status: "todo", Kind: "requirement", WorkflowID: "loop-a", AssigneeUserID: user.ID}
			if err := h.repo.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: "draft-chat", ProjectID: "workbench"}); err != nil {
				t.Fatal(err)
			}
			api := New(h.repo, h.engine, tracker.NewService(), "", "")
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"issues_save","arguments":{"body":{"id":"draft-issue","name":"draft-issue","projectId":"workbench","title":"human_bytes 边界","workflowId":"loop-b"}}}}`
			c, rec := workbenchContext(user, http.MethodPost, "/mcp", bytes.NewBufferString(body))
			c.Request.Header.Set("Content-Type", "application/json")
			if tc.confirm {
				c.Request.Header.Set(chatSessionHeader, "draft-chat")
			}
			api.ServeHTTP(rec, c.Request)
			var reply struct {
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
				Error any `json:"error"`
			}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.Error != nil || reply.Result.IsError {
				t.Fatalf("issues_save returned %d: %s", rec.Code, rec.Body.String())
			}
			got, ok := h.repo.Issue(issue.ID)
			if !ok || got.WorkflowID != "loop-b" {
				t.Fatalf("LOOP was not switched: %+v", got)
			}
			kept := got.Description == issue.Description && got.Priority == issue.Priority && got.Status == issue.Status
			if kept != tc.wantKept {
				t.Fatalf("description=%q priority=%q status=%q, want kept=%v", got.Description, got.Priority, got.Status, tc.wantKept)
			}
		})
	}
}

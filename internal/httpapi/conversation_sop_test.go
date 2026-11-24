package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

func TestConversationSOPDispatchUsesBoundWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		native, share    bool
		revokeGrant      bool
	}{
		{"confirmed issue dispatch", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"issues_dispatch","arguments":{"path":{"id":"handoff-issue"}}}}`, false, true, false},
		{"automatic issue dispatch", "/api/issues/handoff-issue/dispatch", `{}`, true, true, false},
		{"direct SOP start", "/api/runs", `{"projectId":"workbench","workflowId":"handoff-sop"}`, false, true, false},
		{"ordinary issue dispatch", "/api/issues/handoff-issue/dispatch", `{}`, false, false, false},
		{"revoked SOP start", "/api/runs", `{"projectId":"workbench","workflowId":"handoff-sop"}`, false, true, true},
		{"revoked issue dispatch", "/api/issues/handoff-issue/dispatch", `{}`, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user, _ := chatSandboxFixture(t)
			h.engine.ConfigureExecution("api", "api", 30*time.Second, false)
			wf := domain.WorkflowDefinition{
				ID: "handoff-sop", Name: "SOP", ProjectID: "workbench", SandboxProfileID: repo.TestNativeSandboxID,
				Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "command", Command: "true"}},
			}
			if err := h.repo.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			issue := domain.Issue{ID: "handoff-issue", Title: "Fix", ProjectID: wf.ProjectID, WorkflowID: wf.ID, AssigneeUserID: user.ID}
			if err := h.repo.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			runner := domain.Runner{ID: "conversation-machine", OwnerUserID: user.ID, Status: domain.RunnerStatusOnline, RunnerVersion: "0.12.0"}
			if err := h.repo.PutRunner(runner); err != nil {
				t.Fatal(err)
			}
			if err := h.repo.SetRunnerSharedProjects(runner.ID, user.ID, []string{wf.ProjectID}); err != nil {
				t.Fatal(err)
			}
			owner, err := h.engine.StartConversationWorkspace("Conversation", domain.TelemetryContext{
				ProjectID: wf.ProjectID, ResponsibleUserID: user.ID, ExecutionPinned: true, ExecutionRunnerID: runner.ID,
			}, "business")
			if err != nil {
				t.Fatal(err)
			}
			const sessionID = "handoff-chat"
			if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{
				ID: sessionID, ProjectID: wf.ProjectID, WorkspaceRunID: owner.ID, OwnWorkspaceRunIDs: []string{owner.ID},
			}); err != nil {
				t.Fatal(err)
			}
			if tc.revokeGrant {
				if err := h.repo.SetRunnerSharedProjects(runner.ID, user.ID, nil); err != nil {
					t.Fatal(err)
				}
			}
			api := New(h.repo, h.engine, tracker.NewService(), "", "")
			c, rec := workbenchContext(user, http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			if tc.native {
				ctx := withConversationSession(context.Background(), sessionID)
				assistant := &assistantHandlers{deps: h.deps, api: api}
				_, err := assistant.callAssistantPlatform(withAssistantExecMode(ctx, assistantExecMode{AutoWrite: true}), user, wf.ProjectID,
					assistantMCPCall{Tool: "issues_dispatch", Arguments: map[string]any{"id": issue.ID}})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if tc.share {
					c.Request.Header.Set(chatSessionHeader, sessionID)
				}
				api.ServeHTTP(rec, c.Request)
				if tc.revokeGrant {
					if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte(repo.ErrRunnerProjectDenied.Error())) {
						t.Fatalf("dispatch with revoked runner access returned %d: %s", rec.Code, rec.Body)
					}
				} else if rec.Code < 200 || rec.Code >= 300 || bytes.Contains(rec.Body.Bytes(), []byte(`"isError":true`)) {
					t.Fatalf("dispatch returned %d: %s", rec.Code, rec.Body.String())
				}
			}
			var sop domain.Run
			for _, run := range h.repo.Runs() {
				if run.WorkflowID == wf.ID {
					sop = run
				}
			}
			if tc.revokeGrant {
				if sop.ID != "" || h.repo.IssueHasTaskForWorkflow(issue.ID, wf.ID) {
					t.Fatal("dispatch with revoked runner access left a run or task")
				}
				return
			}
			if sop.ID == "" {
				t.Fatal("SOP was not started")
			}
			if !tc.share {
				if sop.ExecutionContext.WorkspaceRunID != "" || h.engine.RunExecutionRunner(sop) != "" {
					t.Fatalf("ordinary dispatch shared a workspace: %+v", sop.ExecutionContext)
				}
				return
			}
			if sop.ExecutionContext.WorkspaceRunID != owner.ID || h.engine.RunExecutionRunner(sop) != runner.ID {
				t.Fatalf("SOP did not use workspace %s on %s: %+v", owner.ID, runner.ID, sop.ExecutionContext)
			}
			if sop.WorkflowSnapshot.SandboxProfileID != wf.SandboxProfileID {
				t.Fatalf("SOP sandbox was replaced by %s", sop.WorkflowSnapshot.SandboxProfileID)
			}
			if err := h.deleteConversation(user.ID, sessionID); err != nil {
				t.Fatal(err)
			}
			if !h.repo.RunExists(owner.ID) {
				t.Fatal("conversation deletion removed a workspace still used by its SOP")
			}
		})
	}
}

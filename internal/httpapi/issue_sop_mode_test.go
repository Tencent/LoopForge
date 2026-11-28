package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestRequirementWithoutSOP(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "sop-mode-flow", Name: "LOOP mode", Version: 1, SandboxProfileID: "test-native", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "sop-mode", Name: "LOOP mode", OwnerID: 1, WorkflowID: "sop-mode-flow"}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := New(st, e, tracker.NewService(), "", "")

	bound := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "sop-mode", "title": "默认绑定", "kind": "requirement",
	}, http.StatusOK)
	if bound.WorkflowID != "sop-mode-flow" || bound.SOPMode != "" {
		t.Fatalf("requirement without a choice must keep the project LOOP: %+v", bound)
	}

	created := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "sop-mode", "title": "不走 LOOP", "kind": "requirement", "sopMode": "none", "workflowId": "sop-mode-flow",
	}, http.StatusOK)
	if created.SOPMode != domain.IssueSOPNone || created.WorkflowID != "" || created.PlaybookID != "" || created.AutomationState != "ignored" {
		t.Fatalf("no-LOOP requirement must not bind a LOOP: %+v", created)
	}
	edited := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": created.ID, "title": "不走 LOOP，补充描述", "description": "细节", "workflowId": "sop-mode-flow",
	}, http.StatusOK)
	if edited.SOPMode != domain.IssueSOPNone || edited.WorkflowID != "" {
		t.Fatalf("edits must keep the no-LOOP choice: %+v", edited)
	}

	d := &deps{repo: st, engine: e}
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: created.ID, ScheduleJobID: "job-1"}); cvBHTTPStatusRound2(err) != http.StatusConflict {
		t.Fatalf("scheduled LOOPs must not claim a no-LOOP requirement: %v", err)
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/"+created.ID+"/dispatch", map[string]any{}, http.StatusBadRequest)
	if _, err := dispatchIssue(d, dispatchRequest{IssueID: created.ID, WorkflowID: "sop-mode-missing"}); err == nil {
		t.Fatal("a missing LOOP started")
	}
	failed, _ := st.Issue(created.ID)
	if failed.SOPMode != domain.IssueSOPNone || failed.WorkflowID != "" || failed.AutomationState != "ignored" {
		t.Fatalf("a failed start must keep the no-LOOP choice: %+v", failed)
	}

	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+created.ID+"/dispatch", map[string]any{"workflowId": "sop-mode-flow"}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	stored, _ := st.Issue(created.ID)
	if task.WorkflowID != "sop-mode-flow" || stored.SOPMode != "" || stored.WorkflowID != "sop-mode-flow" || stored.AutomationState != "eligible" {
		t.Fatalf("running a chosen LOOP must switch the requirement to LOOP delivery: task=%+v issue=%+v", task, stored)
	}

	other := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "sop-mode", "title": "稍后绑定", "kind": "requirement", "sopMode": "none",
	}, http.StatusOK)
	rebound := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues/"+other.ID+"/sops", map[string]any{
		"bindings": []map[string]any{{"workflowId": "sop-mode-flow", "order": 1}},
	}, http.StatusOK)
	if rebound.SOPMode != "" || rebound.WorkflowID != "sop-mode-flow" || rebound.AutomationState != "eligible" {
		t.Fatalf("binding a LOOP must switch the requirement to LOOP delivery: %+v", rebound)
	}
}

// sopMode=sop is how issues_save lifts a no-LOOP choice (see the Issue schema in
// docs/api/openapi.yaml): the requirement gets the project LOOP back and
// scheduled LOOPs may claim it again, as when a LOOP is bound or run from the
// page. A requirement that was ignored on its own keeps that choice.
func TestRequirementBackToSOPRejoinsScheduledClaims(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "sop-back-flow", Name: "LOOP back", Version: 1, SandboxProfileID: "test-native", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "sop-back", Name: "LOOP back", OwnerID: 1, WorkflowID: "sop-back-flow"}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := New(st, e, tracker.NewService(), "", "")

	none := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "sop-back", "title": "先不走 LOOP", "kind": "requirement", "sopMode": "none",
	}, http.StatusOK)
	back := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": none.ID, "projectId": "sop-back", "title": "先不走 LOOP", "sopMode": "sop",
	}, http.StatusOK)
	if back.SOPMode != "" || back.WorkflowID != "sop-back-flow" || back.AutomationState != "eligible" {
		t.Fatalf("sopMode=sop must restore LOOP delivery and scheduled claims: %+v", back)
	}
	d := &deps{repo: st, engine: e}
	task, err := dispatchIssue(d, dispatchRequest{IssueID: none.ID, ScheduleJobID: "job-back"})
	if err != nil {
		t.Fatalf("a scheduled LOOP must claim the requirement again: %v", err)
	}
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })

	ignored := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "sop-back", "title": "手动忽略", "kind": "requirement", "automationState": "ignored",
	}, http.StatusOK)
	kept := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": ignored.ID, "projectId": "sop-back", "title": "手动忽略", "sopMode": "sop",
	}, http.StatusOK)
	if kept.AutomationState != "ignored" || kept.WorkflowID != "sop-back-flow" {
		t.Fatalf("a LOOP requirement ignored on its own must stay ignored: %+v", kept)
	}
	explicit := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "sop-back", "title": "回到 LOOP 但仍忽略", "kind": "requirement", "sopMode": "none",
	}, http.StatusOK)
	stay := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": explicit.ID, "projectId": "sop-back", "title": "回到 LOOP 但仍忽略", "sopMode": "sop", "automationState": "ignored",
	}, http.StatusOK)
	if stay.AutomationState != "ignored" || stay.WorkflowID != "sop-back-flow" {
		t.Fatalf("an explicit automationState must win: %+v", stay)
	}
}

func TestChatRequirementBindsOnlySelectedLOOP(t *testing.T) {
	h, user := workbenchFixture(t)
	e := engine.New(h.repo, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h.api = New(h.repo, e, tracker.NewService(), "", "")
	const projectID, defaultID, chosenID = "workbench", "project-default", "chosen"
	for _, id := range []string{defaultID, chosenID} {
		wf := domain.WorkflowDefinition{ID: id, Name: id, ProjectID: projectID, SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "approval"}}}
		if err := h.repo.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	project, _ := h.repo.Project(projectID)
	project.WorkflowID = defaultID
	project.SOPPlaybooks = []domain.SOPPlaybook{
		{ID: "usual", Name: "Usual", Default: true, Bindings: []domain.IssueSOPBinding{{WorkflowID: defaultID}}},
		{ID: "selected", Name: "Selected", Bindings: []domain.IssueSOPBinding{{WorkflowID: chosenID}}},
	}
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	ctx := withAssistantExecMode(withConversationSession(t.Context(), "chat-room"), assistantExecMode{AutoWrite: true})
	save := func(t *testing.T, title string, fields map[string]any) domain.Issue {
		t.Helper()
		body := map[string]any{"projectId": projectID, "kind": "requirement", "title": title, "status": "todo"}
		for k, v := range fields {
			body[k] = v
		}
		result, err := h.callAssistantPlatform(ctx, user, projectID, assistantMCPCall{Server: "agentflow", Tool: "issues_save", Arguments: map[string]any{"body": body}})
		if err != nil {
			t.Fatal(err)
		}
		var saved domain.Issue
		if err := json.Unmarshal([]byte(result), &saved); err != nil || saved.ID == "" {
			t.Fatalf("issues_save response: %s (%v)", result, err)
		}
		stored, ok := h.repo.Issue(saved.ID)
		if !ok {
			t.Fatal("missing issue after issues_save")
		}
		return stored
	}
	for _, tc := range []struct {
		name     string
		fields   map[string]any
		workflow string
	}{
		{"unspecified", nil, ""},
		{"without LOOP", map[string]any{"sopMode": "none", "workflowId": chosenID}, ""},
		{"named LOOP", map[string]any{"workflowId": chosenID}, chosenID},
		{"selected playbook", map[string]any{"playbookId": "selected"}, chosenID},
		{"selected steps", map[string]any{"sopBindings": []domain.IssueSOPBinding{{WorkflowID: chosenID}}}, chosenID},
		{"opted into project default", map[string]any{"sopMode": "sop"}, defaultID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := save(t, tc.name, tc.fields)
			if item.WorkflowID != tc.workflow || item.WithoutSOP() != (tc.workflow == "") {
				t.Fatalf("workflow=%q mode=%q, want workflow=%q", item.WorkflowID, item.SOPMode, tc.workflow)
			}
			if has, err := h.repo.IssueHasTasks(item.ID); err != nil || has {
				t.Fatalf("save created a task: %v, %v", has, err)
			}
			if tc.workflow == "" {
				if item.PlaybookID != "" || item.AutomationState != "ignored" || len(item.EffectiveSOPBindings()) != 0 {
					t.Fatalf("unexpected delivery settings: %+v", item)
				}
				_, err := dispatchIssue(&deps{repo: h.repo, engine: e}, dispatchRequest{IssueID: item.ID, ScheduleJobID: "scheduled", WorkflowID: defaultID})
				if cvBHTTPStatusRound2(err) != http.StatusConflict {
					t.Fatalf("scheduled dispatch: %v", err)
				}
			}
			updated := save(t, "Clarified "+tc.name, map[string]any{"id": item.ID})
			if updated.WorkflowID != item.WorkflowID || updated.SOPMode != item.SOPMode {
				t.Fatalf("clarification changed binding to %q, mode %q", updated.WorkflowID, updated.SOPMode)
			}
		})
	}

	t.Run("browser-confirmed draft", func(t *testing.T) {
		args := map[string]any{"body": map[string]any{"projectId": projectID, "kind": "requirement", "title": "Confirmed in chatbot"}}
		payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "issues_save", "arguments": args}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(chatSessionHeader, "chat-room")
		req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(user)))
		rec := httptest.NewRecorder()
		h.api.ServeHTTP(rec, req)
		var response struct {
			Result struct {
				IsError           bool
				StructuredContent struct{ Data domain.Issue }
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || response.Result.IsError {
			t.Fatalf("MCP response %d: %s", rec.Code, rec.Body.String())
		}
		got, ok := h.repo.Issue(response.Result.StructuredContent.Data.ID)
		if !ok || !got.WithoutSOP() || got.WorkflowID != "" || got.AutomationState != "ignored" {
			t.Fatalf("confirmed requirement: id=%q mode=%q workflow=%q", got.ID, got.SOPMode, got.WorkflowID)
		}
	})
}

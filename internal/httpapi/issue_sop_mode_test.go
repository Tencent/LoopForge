package httpapi

import (
	"net/http"
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

// The page editor echoes the stored sopMode. Saving a no-LOOP requirement with
// a workflowId keeps it without a LOOP, so the audit log must not claim a LOOP
// was bound; sending sopMode=sop with the chosen LOOP binds it and says so.
func TestRequirementWithoutSOPEditAuditMatchesStoredLOOP(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, id := range []string{"sop-audit-default", "sop-audit-chosen"} {
		if err := st.PutWorkflow(domain.WorkflowDefinition{ID: id, Name: id, Version: 1, SandboxProfileID: "test-native", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutProject(domain.Project{ID: "sop-audit", Name: "LOOP audit", OwnerID: 1, WorkflowID: "sop-audit-default"}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := New(st, e, tracker.NewService(), "", "")
	lastDetail := func(issue domain.Issue) string {
		if len(issue.Operations) == 0 {
			return ""
		}
		return issue.Operations[len(issue.Operations)-1].Detail
	}

	none := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "sop-audit", "title": "先不走 LOOP", "kind": "requirement", "sopMode": "none",
	}, http.StatusOK)
	echoed := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": none.ID, "projectId": "sop-audit", "title": "先不走 LOOP", "sopMode": "none", "workflowId": "sop-audit-chosen", "priority": "high",
	}, http.StatusOK)
	if echoed.SOPMode != domain.IssueSOPNone || echoed.WorkflowID != "" {
		t.Fatalf("an echoed sopMode=none keeps the requirement without a LOOP: %+v", echoed)
	}
	if detail := lastDetail(echoed); detail != "优先级" {
		t.Fatalf("audit must record only what was stored, got %q", detail)
	}

	bound := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": none.ID, "projectId": "sop-audit", "title": "先不走 LOOP", "sopMode": "sop", "workflowId": "sop-audit-chosen", "automationState": "", "priority": "high",
	}, http.StatusOK)
	if bound.SOPMode != "" || bound.WorkflowID != "sop-audit-chosen" || bound.AutomationState != "eligible" {
		t.Fatalf("sopMode=sop with a chosen LOOP must bind that LOOP and rejoin scheduled claims: %+v", bound)
	}
	if detail := lastDetail(bound); detail != "关联 LOOP" {
		t.Fatalf("binding a LOOP must be audited, got %q", detail)
	}
}

package domain

import "testing"

func TestEffectiveSOPBindingsFallsBackToWorkflowID(t *testing.T) {
	issue := Issue{ID: "iss", WorkflowID: "wf-backend", AssigneeUserID: 9}
	got := issue.EffectiveSOPBindings()
	if len(got) != 1 || got[0].WorkflowID != "wf-backend" || got[0].AssigneeUserID != 9 {
		t.Fatalf("got=%#v", got)
	}
}

func TestNormalizeSOPBindingsKeepsOrderAndUniqueWorkflows(t *testing.T) {
	got := NormalizeSOPBindings([]IssueSOPBinding{
		{WorkflowID: "design", Order: 20, AssigneeUserID: 2},
		{WorkflowID: "backend", Order: 10, AssigneeUserID: 2, Handoff: []string{"prd.md", ""}},
		{WorkflowID: "backend", Order: 99},
		{WorkflowID: "  "},
	})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].WorkflowID != "backend" || got[0].Order != 1 || got[0].AssigneeUserID != 2 {
		t.Fatalf("first=%#v", got[0])
	}
	if got[1].WorkflowID != "design" || got[1].Order != 2 {
		t.Fatalf("second=%#v", got[1])
	}
	if len(got[0].Handoff) != 1 || got[0].Handoff[0] != "prd.md" {
		t.Fatalf("handoff=%#v", got[0].Handoff)
	}
}

func TestSOPBindingForWorkflow(t *testing.T) {
	issue := Issue{SOPBindings: []IssueSOPBinding{{WorkflowID: "wf-fe", AssigneeUserID: 3}}}
	got, ok := issue.SOPBindingForWorkflow("wf-fe")
	if !ok || got.AssigneeUserID != 3 {
		t.Fatalf("got=%#v ok=%v", got, ok)
	}
	if _, ok := issue.SOPBindingForWorkflow("wf-be"); ok {
		t.Fatal("missing workflow should not match")
	}
}

func TestResolveSOPBindingsFollowsBoundWorkflow(t *testing.T) {
	project := Project{ID: "p", WorkflowID: "wf-old", SOPBindings: []IssueSOPBinding{
		{WorkflowID: "wf-product", Order: 1},
		{WorkflowID: "wf-backend", Order: 2, AssigneeUserID: 4},
	}}
	issue := Issue{ID: "iss", ProjectID: "p", Kind: "requirement", WorkflowID: "wf-old", AssigneeUserID: 9}
	got := ResolveSOPBindings(issue, project)
	if len(got) != 1 || got[0].WorkflowID != "wf-old" || got[0].AssigneeUserID != 9 {
		t.Fatalf("bound SOP=%#v", got)
	}
	issue.SOPBindings = []IssueSOPBinding{{WorkflowID: "wf-design"}}
	got = ResolveSOPBindings(issue, project)
	if len(got) != 1 || got[0].WorkflowID != "wf-design" {
		t.Fatalf("override=%#v", got)
	}
	bug := Issue{ID: "bug", Kind: "issue", Source: "gongfeng", WorkflowID: "wf-fix"}
	got = ResolveSOPBindings(bug, project)
	if len(got) != 1 || got[0].WorkflowID != "wf-fix" {
		t.Fatalf("gongfeng must stay on its own SOP: %#v", got)
	}
}

func TestPlaybooksWrapsLegacyBindingsAndPicksNamed(t *testing.T) {
	legacy := Project{ID: "p", WorkflowID: "wf-old", SOPBindings: []IssueSOPBinding{
		{WorkflowID: "wf-product", Order: 1},
		{WorkflowID: "wf-backend", Order: 2},
	}}
	books := legacy.Playbooks()
	if len(books) != 1 || books[0].ID != "default" || !books[0].Default || len(books[0].Bindings) != 2 {
		t.Fatalf("legacy wrap=%#v", books)
	}
	project := Project{ID: "p", SOPPlaybooks: []SOPPlaybook{
		{ID: "full", Name: "完整需求", Default: true, Bindings: []IssueSOPBinding{{WorkflowID: "wf-product"}, {WorkflowID: "wf-frontend"}, {WorkflowID: "wf-backend"}}},
		{ID: "be", Name: "仅后端", Bindings: []IssueSOPBinding{{WorkflowID: "wf-backend"}}},
	}}
	issue := Issue{ID: "iss", ProjectID: "p", Kind: "requirement", WorkflowID: "wf-backend", PlaybookID: "be"}
	got := ResolveSOPBindings(issue, project)
	if len(got) != 1 || got[0].WorkflowID != "wf-backend" {
		t.Fatalf("bound SOP=%#v", got)
	}
	issue.PlaybookID = ""
	issue.WorkflowID = "wf-frontend"
	got = ResolveSOPBindings(issue, project)
	if len(got) != 1 || got[0].WorkflowID != "wf-frontend" {
		t.Fatalf("progress must follow workflowId, not playbook: %#v", got)
	}
}

package httpapi

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestWorkflowDeleteWarnings(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutProject(domain.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	h := &assistantHandlers{deps: &deps{repo: st}}

	if warnings := h.workflowDeleteWarnings("no-such"); len(warnings) != 1 || !strings.Contains(warnings[0], "不存在") {
		t.Fatalf("missing workflow warnings = %#v", warnings)
	}

	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "wf", Name: "WF", ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	if warnings := h.workflowDeleteWarnings("wf"); len(warnings) != 0 {
		t.Fatalf("unused workflow should have no warnings: %#v", warnings)
	}

	for _, run := range []domain.Run{
		{ID: "r-running", WorkflowID: "wf", Status: "running", Nodes: map[string]*domain.NodeRun{}},
		{ID: "r-waiting", WorkflowID: "wf", Status: "waiting", Nodes: map[string]*domain.NodeRun{}},
		{ID: "r-done", WorkflowID: "wf", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}},
	} {
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	for _, issue := range []domain.Issue{
		{ID: "i-open", ProjectID: "p", Title: "open", Kind: "requirement", Source: "local", Status: "todo", WorkflowID: "wf"},
		{ID: "i-closed", ProjectID: "p", Title: "closed", Kind: "requirement", Source: "local", Status: "closed", WorkflowID: "wf"},
	} {
		if err := st.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	warnings := h.workflowDeleteWarnings("wf")
	if len(warnings) != 2 {
		t.Fatalf("warnings = %#v", warnings)
	}
	if !strings.Contains(warnings[0], "还有 2 个进行中的运行") {
		t.Fatalf("running warning = %q", warnings[0])
	}
	if !strings.Contains(warnings[1], "1 个未关闭的 Issue 绑定（i-open）") {
		t.Fatalf("bound issue warning = %q", warnings[1])
	}
	if got := h.assistantDraftWarnings(assistantDraft{Tool: "workflows_delete", Query: map[string]any{"id": "wf"}}); strings.Join(got, "|") != strings.Join(warnings, "|") {
		t.Fatalf("assistantDraftWarnings = %#v, want %#v", got, warnings)
	}
}

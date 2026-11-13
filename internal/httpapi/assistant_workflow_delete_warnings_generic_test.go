package httpapi

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// A generic template must be flagged as undeletable, and runs / issues bound
// to a different SOP must not leak into its counts.
func TestWorkflowDeleteWarningsGenericTemplateAndOtherWorkflowsIgnored(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutProject(domain.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGenericTemplate(repo.GenericTemplate{Workflow: domain.WorkflowDefinition{
		ID: "gen-wf", Name: "通用", Version: 1, Status: "published", Generic: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: "other", Name: "Other", ProjectID: "p"}); err != nil {
		t.Fatal(err)
	}
	h := &assistantHandlers{deps: &deps{repo: st}}

	warnings := h.workflowDeleteWarnings("gen-wf")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "通用模板不可删除") {
		t.Fatalf("generic template warnings = %#v", warnings)
	}

	// Active runs and open issues on another SOP do not count against gen-wf
	// or a plain unused SOP.
	for _, run := range []domain.Run{
		{ID: "r-other", WorkflowID: "other", Status: "running", Nodes: map[string]*domain.NodeRun{}},
		{ID: "r-gen", WorkflowID: "gen-wf", Status: "waiting", Nodes: map[string]*domain.NodeRun{}},
	} {
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutIssue(domain.Issue{ID: "i-other", ProjectID: "p", Title: "o", Kind: "requirement", Source: "local", Status: "todo", WorkflowID: "other"}); err != nil {
		t.Fatal(err)
	}

	warnings = h.workflowDeleteWarnings("gen-wf")
	if len(warnings) != 2 {
		t.Fatalf("generic template with a waiting run warnings = %#v", warnings)
	}
	if !strings.Contains(warnings[0], "通用模板不可删除") || !strings.Contains(warnings[1], "还有 1 个进行中的运行") {
		t.Fatalf("warnings order/content = %#v", warnings)
	}
	for _, w := range warnings {
		if strings.Contains(w, "Issue") {
			t.Fatalf("issue bound to another SOP leaked into gen-wf warnings: %#v", warnings)
		}
	}

	otherWarnings := h.workflowDeleteWarnings("other")
	if len(otherWarnings) != 2 || strings.Contains(strings.Join(otherWarnings, "|"), "通用模板") {
		t.Fatalf("other workflow warnings = %#v", otherWarnings)
	}
	if !strings.Contains(otherWarnings[0], "还有 1 个进行中的运行") || !strings.Contains(otherWarnings[1], "1 个未关闭的 Issue 绑定（i-other）") {
		t.Fatalf("other workflow warnings = %#v", otherWarnings)
	}
}

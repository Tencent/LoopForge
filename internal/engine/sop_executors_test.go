package engine

import (
	"errors"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestGenericSOPCopyUsesPersonalDefaultForUnpinnedRoles(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutGenericTemplate(repo.GenericTemplate{
		Workflow: domain.WorkflowDefinition{ID: "sop-template", Name: "Template"},
		Agents:   []domain.AgentDefinition{{ID: "reviewer"}, {ID: "author"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []domain.AgentDefinition{
		{ID: "reviewer-beta", Name: "Reviewer", ProjectID: "beta", ExecutorID: "codebuddy"},
		{ID: "author", Name: "Author", ProjectID: "beta", ExecutorID: "codebuddy", Model: "explicit-model"},
		{ID: "custom", Name: "Custom role", ProjectID: "beta", ExecutorID: "codebuddy"},
	} {
		if err := st.PutAgent(agent); err != nil {
			t.Fatal(err)
		}
	}
	workflow := domain.WorkflowDefinition{
		ID: "copy", ProjectID: "beta", Tags: []string{"generic-source:sop-template"},
		Nodes: []domain.WorkflowNode{
			{ID: "review", Type: "agent", AgentID: "reviewer-beta"},
			{ID: "author", Type: "agent", AgentID: "author"},
			{ID: "custom", Type: "agent", AgentID: "custom"},
		},
	}
	e := &Engine{repo: st}
	execution := domain.TelemetryContext{ResponsibleUserID: 1}
	workflow.ExecutorDefaults, _, err = e.planSOPExecutors(workflow, execution, false)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "run-deferred", WorkflowID: workflow.ID, WorkflowSnapshot: workflow, ExecutionContext: execution, Status: "running",
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowSandboxRequirements(st, workflow); err != nil {
		t.Fatalf("an unresolved role should not prevent workspace preparation: %v", err)
	}
	if _, err := e.resolveDeferredSOPExecutor(run, workflow.Nodes[0]); !errors.Is(err, errSOPExecutorUnconfigured) {
		t.Fatalf("the role should report missing configuration before invoking a CLI: %v", err)
	}
	defaults := domain.AssistantDefaults{Executor: "dsh", Model: "deepseek-v4.1-flash"}
	if _, err := st.UpdateUserCredentials(1, repo.UserCredentialUpdate{
		AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{"dsh": {APIKey: "fixture-key"}},
		AssistantDefaults:   &defaults,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.resolveDeferredSOPExecutor(run, workflow.Nodes[0]); err != nil {
		t.Fatal(err)
	}
	stored, ok := st.RunState(run.ID)
	if !ok {
		t.Fatal("run not found")
	}
	workflow = stored.WorkflowSnapshot
	if len(workflow.ExecutorDefaults) != 1 || workflow.ExecutorDefaults["review"] != defaults {
		t.Fatalf("only the inherited template role should use personal defaults: %+v", workflow.ExecutorDefaults)
	}
	for i, want := range []domain.ExecutorDefinition{
		{Kind: "dsh", Model: "deepseek-v4.1-flash"},
		{Kind: "codebuddy", Model: "explicit-model"},
		{Kind: "codebuddy"},
	} {
		node := workflow.Nodes[i]
		role, _ := workflowAgent(st, workflow, node.AgentID)
		got, err := workflowNodeExecutor(st, workflow, node, role)
		if err != nil || got.Kind != want.Kind || got.Model != want.Model {
			t.Fatalf("%s executor = %+v, err = %v; want %s / %s", node.ID, got, err, want.Kind, want.Model)
		}
	}
}

func TestGenericSOPExecutorPrefersRunnerLoginOverSharedKey(t *testing.T) {
	access := sopExecutorAccess{
		usesRunner:      true,
		sharedCodeBuddy: true,
		runner: domain.Runner{Runtimes: []domain.RunnerRuntime{
			{Provider: "codebuddy", AuthMode: domain.RuntimeAuthPersonal, Status: domain.RuntimeStatusOnline},
			{Provider: "codex", AuthMode: domain.RuntimeAuthLocal, Status: domain.RuntimeStatusOnline},
		}},
	}
	if got := access.defaults().Executor; got != "codex" {
		t.Fatalf("executor with configured machine login = %s, want codex", got)
	}
}

func TestSOPExecutorDefaultForBaselinePreviewAndProjectRole(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	// Existing installations may still have the seeded CodeBuddy resolver.
	baseline := repo.BaselineAlignResolverAgent()
	baseline.ExecutorID = "codebuddy"
	for _, role := range []domain.AgentDefinition{
		baseline,
		{ID: "project-reviewer", Name: "Reviewer", ProjectID: "demo"},
		{ID: "pinned", Name: "Pinned", ProjectID: "demo", ExecutorID: "codebuddy"},
	} {
		if err := st.PutAgent(role); err != nil {
			t.Fatal(err)
		}
	}
	preference := domain.AssistantDefaults{Executor: "codex", Model: "fixture-model"}
	if _, err := st.UpdateUserCredentials(1, repo.UserCredentialUpdate{
		CodexAPIKey: "fixture-key", AssistantDefaults: &preference,
	}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{repo: st}
	baselineFlow := domain.WorkflowDefinition{ID: BaselineAlignWorkflowID, Nodes: []domain.WorkflowNode{
		{ID: "resolve", Type: "agent", AgentID: baseline.ID},
	}}
	t.Setenv("AGENTFLOW_BASELINE_ALIGN_EXECUTOR", "")
	requirements, err := e.SOPExecutorRequirements(baselineFlow, 1, "demo")
	if err != nil || len(requirements) != 1 || requirements[0].Kind != "codex" || !requirements[0].Configured {
		t.Fatalf("baseline preflight = %+v, %v", requirements, err)
	}
	t.Setenv("AGENTFLOW_BASELINE_ALIGN_EXECUTOR", "claude")
	requirements, err = e.SOPExecutorRequirements(baselineFlow, 1, "demo")
	if err != nil || len(requirements) != 1 || requirements[0].Kind != "claude-code" {
		t.Fatalf("baseline override = %+v, %v", requirements, err)
	}

	projectFlow, err := domain.AttachPreviewBuild(domain.WorkflowDefinition{
		ID: "project-flow", ProjectID: "demo", Preview: &domain.PreviewContract{Enabled: true},
		Nodes: []domain.WorkflowNode{
			{ID: "review", Type: "agent", AgentID: "project-reviewer"},
			{ID: "pinned", Type: "agent", AgentID: "pinned"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defaults, _, err := e.planSOPExecutors(projectFlow, domain.TelemetryContext{ResponsibleUserID: 1}, false)
	if err != nil || len(defaults) != 2 ||
		defaults["review"] != preference || defaults[domain.PreviewBuildNodeID] != preference {
		t.Fatalf("project role and preview defaults = %+v, %v", defaults, err)
	}
}

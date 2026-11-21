package engine

import (
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
	defaults := domain.AssistantDefaults{Executor: "dsh", Model: "deepseek-v4.1-flash"}
	if _, err := st.UpdateUserCredentials(1, repo.UserCredentialUpdate{
		AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{"dsh": {APIKey: "fixture-key"}},
		AssistantDefaults:   &defaults,
	}); err != nil {
		t.Fatal(err)
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
	workflow.ExecutorDefaults, _, err = e.planSOPExecutors(workflow, domain.TelemetryContext{ResponsibleUserID: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
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

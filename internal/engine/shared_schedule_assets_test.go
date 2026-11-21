package engine

import (
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSharedScheduleUsesNormalProjectAssetResolution(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(st.PutRule(domain.RuleDefinition{ID: "rule", Name: "Rule", ProjectID: "source", Shared: true, Content: "shared source rule"}))
	check(st.PutSkill(domain.SkillDefinition{ID: "skill", Name: "Skill", ProjectID: "source", Shared: true, Content: "shared source skill"}))
	check(st.PutMCPServer(domain.MCPServerDefinition{ID: "source-tool", Name: "Source tool", Scope: "project", ProjectID: "source", Transport: "http", URL: "https://example.com/mcp"}))
	check(st.PutMCPServer(domain.MCPServerDefinition{ID: "target-tool", Name: "Target tool", Scope: "project", ProjectID: "target", Transport: "http", URL: "https://example.com/mcp"}))
	role := domain.AgentDefinition{ID: "role", Name: "Role", ProjectID: "source", Shared: true, Rules: []string{"rule"}, Skills: []string{"skill"}, MCPServers: []string{"source-tool", "target-tool"}}
	check(st.PutAgent(role))
	job := domain.ScheduleJob{ID: "job", Name: "Job", ProjectID: "source", SkipDispatch: true, AssistantPrompt: "inspect", References: []domain.AssistantReference{{Type: "agent", ID: role.ID}}}
	check(st.PutScheduleJob(job))
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0)
	check(err)
	check(st.ValidateSharedScheduleAssets(v))
	e := New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	bundle := e.RoleAssets(role, "target", 1)
	if len(bundle.Rules) != 1 || len(bundle.Skills) != 1 || len(bundle.MCPServers) != 1 || bundle.MCPServers[0].ID != "target-tool" {
		t.Fatalf("wrong project-scoped bundle: %+v", bundle)
	}
	check(st.PutRule(domain.RuleDefinition{ID: "target-policy", Name: "Target policy", ProjectID: "target", Content: "target policy", Global: true, WorkflowIDs: []string{"sop"}}))
	e.appendWorkflowGlobalRules(&bundle, "target", "sop")
	if len(bundle.Rules) != 2 || bundle.Rules[1].ID != "target-policy" {
		t.Fatalf("missing target SOP policy: %+v", bundle.Rules)
	}
}

func TestSharedScheduleNestedSOPUsesImmediateParentBinding(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, wf := range []domain.WorkflowDefinition{
		{ID: "root", Name: "Root", ProjectID: "source", Shared: true, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "child-node", Name: "Child", Type: "sop", SOPWorkflowID: "child"}}},
		{ID: "child", Name: "Child", ProjectID: "source", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "leaf-node", Name: "Leaf", Type: "sop", SOPWorkflowID: "leaf"}}},
		{ID: "leaf", Name: "Leaf", ProjectID: "source", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approve", Name: "Approve", Type: "approval"}}},
	} {
		check(st.PutWorkflow(wf))
	}
	job := domain.ScheduleJob{ID: "nested-job", Name: "Nested", ProjectID: "source", SkipDispatch: true, AssistantPrompt: "start", References: []domain.AssistantReference{{Type: "sop", ID: "root"}}}
	check(st.PutScheduleJob(job))
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Nested", Prompt: "start"}, 0)
	check(err)
	e := New(st, runtime.NewManager(t.TempDir()))
	// The control-plane start path persists real snapshots without launching
	// background executions, so each admission can be checked independently.
	e.ConfigureExecution("test", "test", time.Second, false)
	exec := domain.TelemetryContext{ProjectID: "target", ResponsibleUserID: 42, SharedScheduleID: v.ID, SharedScheduleVersion: v.Version}
	root, err := e.StartWithRepositoriesContext("root", "inspect", nil, exec)
	check(err)
	if _, err := e.StartWithRepositoriesContext("child", "inspect", nil, exec); err == nil {
		t.Fatal("unreferenced child admitted without a parent")
	}
	exec.ParentRunID, exec.ParentNodeID = root.ID, "child-node"
	for name, forged := range map[string]domain.TelemetryContext{
		"wrong node":    func() domain.TelemetryContext { v := exec; v.ParentNodeID = "missing"; return v }(),
		"wrong project": func() domain.TelemetryContext { v := exec; v.ProjectID = "other"; return v }(),
		"wrong actor":   func() domain.TelemetryContext { v := exec; v.ResponsibleUserID = 43; return v }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := e.StartWithRepositoriesContext("child", "inspect", nil, forged); err == nil {
				t.Fatal("forged child context admitted")
			}
		})
	}
	child, err := e.StartWithRepositoriesContext("child", "inspect", nil, exec)
	check(err)
	if _, err := e.StartWithRepositoriesContext("leaf", "inspect", nil, exec); err == nil {
		t.Fatal("root admitted an unbound grandchild")
	}
	exec.ParentRunID, exec.ParentNodeID = child.ID, "leaf-node"
	leaf, err := e.StartWithRepositoriesContext("leaf", "inspect", nil, exec)
	check(err)
	if leaf.ExecutionContext.ProjectID != "target" || leaf.ExecutionContext.ResponsibleUserID != 42 || len(v.Assets) != 1 {
		t.Fatalf("nested execution changed scope or expanded the grant: %+v", leaf.ExecutionContext)
	}
}

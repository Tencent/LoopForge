package engine

import (
	"path/filepath"
	"strings"
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
		{ID: "child", Name: "Child", ProjectID: "source", Shared: true, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "leaf-node", Name: "Leaf", Type: "sop", SOPWorkflowID: "leaf"}}},
		{ID: "leaf", Name: "Leaf", ProjectID: "source", Shared: true, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approve", Name: "Approve", Type: "approval"}}},
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

func TestSharedScheduleRejectsPrivateNestedSOPSandboxResources(t *testing.T) {
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
	check(st.PutEnvironment(domain.EnvironmentDefinition{
		ID: "private-child-env", Name: "Private child environment", ProjectID: "source",
		Variables: map[string]string{"PRIVATE_TOKEN": "do-not-leak"}, InstallScript: "echo private-install",
	}))
	check(st.PutSandbox(domain.SandboxProfile{
		ID: "private-child-sandbox", Name: "Private child sandbox", ProjectID: "source",
		Driver: domain.SandboxDriverNative, EnvironmentID: "private-child-env",
	}))
	check(st.PutWorkflow(domain.WorkflowDefinition{
		ID: "shared-child", Name: "Shared child", ProjectID: "source", Shared: true,
		SandboxProfileID: "private-child-sandbox",
		Nodes:            []domain.WorkflowNode{{ID: "approve", Name: "Approve", Type: "approval"}},
	}))
	check(st.PutWorkflow(domain.WorkflowDefinition{
		ID: "shared-parent", Name: "Shared parent", ProjectID: "source", Shared: true,
		SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes:            []domain.WorkflowNode{{ID: "child", Name: "Child", Type: "sop", SOPWorkflowID: "shared-child"}},
	}))
	job := domain.ScheduleJob{
		ID: "private-nested-job", Name: "Private nested", ProjectID: "source", SkipDispatch: true,
		AssistantPrompt: "start", References: []domain.AssistantReference{{Type: "sop", ID: "shared-parent"}},
	}
	check(st.PutScheduleJob(job))
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Private nested", Prompt: "start"}, 0)
	check(err)
	e := New(st, runtime.NewManager(t.TempDir()))
	e.ConfigureExecution("test", "test", time.Second, false)
	exec := domain.TelemetryContext{ProjectID: "target", ResponsibleUserID: 42, SharedScheduleID: v.ID, SharedScheduleVersion: v.Version}
	parent, err := e.StartWithRepositoriesContext("shared-parent", "inspect", nil, exec)
	check(err)
	exec.ParentRunID, exec.ParentNodeID = parent.ID, "child"
	if _, err := e.StartWithRepositoriesContext("shared-child", "inspect", nil, exec); err == nil || !strings.Contains(err.Error(), "沙箱") {
		t.Fatalf("private nested sandbox was admitted: %v", err)
	}
	if runs := st.Runs(); len(runs) != 1 {
		t.Fatalf("rejected child created a run or leaked its snapshot: %d runs", len(runs))
	}
}

func TestSharedScheduleRejectsPrivateNestedSOP(t *testing.T) {
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
	check(st.PutWorkflow(domain.WorkflowDefinition{
		ID: "private-child", Name: "Private child", ProjectID: "source",
		SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes:            []domain.WorkflowNode{{ID: "approve", Name: "Approve", Type: "approval"}},
	}))
	check(st.PutWorkflow(domain.WorkflowDefinition{
		ID: "shared-parent-for-private-child", Name: "Shared parent", ProjectID: "source", Shared: true,
		SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes:            []domain.WorkflowNode{{ID: "child", Name: "Child", Type: "sop", SOPWorkflowID: "private-child"}},
	}))
	job := domain.ScheduleJob{
		ID: "private-child-job", Name: "Private child", ProjectID: "source", SkipDispatch: true,
		AssistantPrompt: "start", References: []domain.AssistantReference{{Type: "sop", ID: "shared-parent-for-private-child"}},
	}
	check(st.PutScheduleJob(job))
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Private child", Prompt: "start"}, 0)
	check(err)
	e := New(st, runtime.NewManager(t.TempDir()))
	e.ConfigureExecution("test", "test", time.Second, false)
	exec := domain.TelemetryContext{ProjectID: "target", ResponsibleUserID: 42, SharedScheduleID: v.ID, SharedScheduleVersion: v.Version}
	parent, err := e.StartWithRepositoriesContext("shared-parent-for-private-child", "inspect", nil, exec)
	check(err)
	exec.ParentRunID, exec.ParentNodeID = parent.ID, "child"
	if _, err := e.StartWithRepositoriesContext("private-child", "inspect", nil, exec); err == nil || !strings.Contains(err.Error(), "SOP") {
		t.Fatalf("private nested SOP was admitted: %v", err)
	}
	if runs := st.Runs(); len(runs) != 1 {
		t.Fatalf("rejected child created a run: %d runs", len(runs))
	}
}

func TestSharedScheduleRejectsPrivateNestedDependencies(t *testing.T) {
	tests := []struct {
		name       string
		expected   string
		setupChild func(*repo.Store) (domain.WorkflowDefinition, error)
	}{
		{
			name:     "environment behind shared sandbox",
			expected: "环境",
			setupChild: func(st *repo.Store) (domain.WorkflowDefinition, error) {
				if err := st.PutEnvironment(domain.EnvironmentDefinition{
					ID: "private-environment", Name: "Private environment", ProjectID: "source",
					Variables: map[string]string{"PRIVATE_TOKEN": "do-not-leak"}, InstallScript: "echo private-install",
				}); err != nil {
					return domain.WorkflowDefinition{}, err
				}
				if err := st.PutSandbox(domain.SandboxProfile{
					ID: "shared-sandbox-with-private-environment", Name: "Shared sandbox", ProjectID: "source",
					Shared: true, Driver: domain.SandboxDriverNative, EnvironmentID: "private-environment",
				}); err != nil {
					return domain.WorkflowDefinition{}, err
				}
				return domain.WorkflowDefinition{
					ID: "child-with-private-environment", Name: "Child with private environment", ProjectID: "source", Shared: true,
					SandboxProfileID: "shared-sandbox-with-private-environment",
					Nodes:            []domain.WorkflowNode{{ID: "approve", Name: "Approve", Type: "approval"}},
				}, nil
			},
		},
		{
			name:     "docker sandbox with private dockerfile",
			expected: "Dockerfile",
			setupChild: func(st *repo.Store) (domain.WorkflowDefinition, error) {
				if err := st.PutDockerfile(domain.DockerfileDefinition{
					ID: "private-dockerfile", Name: "Private Dockerfile", ProjectID: "source", Content: "FROM scratch\n",
				}); err != nil {
					return domain.WorkflowDefinition{}, err
				}
				if err := st.PutSandbox(domain.SandboxProfile{
					ID: "shared-docker-sandbox", Name: "Shared Docker sandbox", ProjectID: "source",
					Shared: true, Driver: domain.SandboxDriverDocker, DockerfileID: "private-dockerfile",
				}); err != nil {
					return domain.WorkflowDefinition{}, err
				}
				return domain.WorkflowDefinition{
					ID: "child-with-private-dockerfile", Name: "Child with private Dockerfile", ProjectID: "source", Shared: true,
					SandboxProfileID: "shared-docker-sandbox",
					Nodes:            []domain.WorkflowNode{{ID: "approve", Name: "Approve", Type: "approval"}},
				}, nil
			},
		},
		{
			name:     "private role node",
			expected: "角色",
			setupChild: func(st *repo.Store) (domain.WorkflowDefinition, error) {
				if err := st.PutAgent(domain.AgentDefinition{
					ID: "private-role", Name: "Private role", ProjectID: "source", ExecutorID: "mock",
				}); err != nil {
					return domain.WorkflowDefinition{}, err
				}
				return domain.WorkflowDefinition{
					ID: "child-with-private-role", Name: "Child with private role", ProjectID: "source", Shared: true,
					SandboxProfileID: repo.DefaultSandboxProfileID,
					Nodes:            []domain.WorkflowNode{{ID: "role", Name: "Role", Type: "agent", AgentID: "private-role"}},
				}, nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			child, err := tc.setupChild(st)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.PutWorkflow(child); err != nil {
				t.Fatal(err)
			}
			parent := domain.WorkflowDefinition{
				ID: "shared-parent-" + child.ID, Name: "Shared parent", ProjectID: "source", Shared: true,
				SandboxProfileID: repo.DefaultSandboxProfileID,
				Nodes:            []domain.WorkflowNode{{ID: "child", Name: "Child", Type: "sop", SOPWorkflowID: child.ID}},
			}
			if err := st.PutWorkflow(parent); err != nil {
				t.Fatal(err)
			}
			job := domain.ScheduleJob{
				ID: "job-" + child.ID, Name: "Nested dependency", ProjectID: "source", SkipDispatch: true,
				AssistantPrompt: "start", References: []domain.AssistantReference{{Type: "sop", ID: parent.ID}},
			}
			if err := st.PutScheduleJob(job); err != nil {
				t.Fatal(err)
			}
			version, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: job.Name, Prompt: job.AssistantPrompt}, 0)
			if err != nil {
				t.Fatal(err)
			}

			e := New(st, runtime.NewManager(t.TempDir()))
			e.ConfigureExecution("test", "test", time.Second, false)
			exec := domain.TelemetryContext{ProjectID: "target", ResponsibleUserID: 42, SharedScheduleID: version.ID, SharedScheduleVersion: version.Version}
			parentRun, err := e.StartWithRepositoriesContext(parent.ID, "inspect", nil, exec)
			if err != nil {
				t.Fatal(err)
			}
			exec.ParentRunID, exec.ParentNodeID = parentRun.ID, "child"
			if _, err := e.StartWithRepositoriesContext(child.ID, "inspect", nil, exec); err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("private nested dependency was admitted: %v", err)
			}
			if runs := st.Runs(); len(runs) != 1 {
				t.Fatalf("rejected child created a run or leaked its snapshot: %d runs", len(runs))
			}
		})
	}
}

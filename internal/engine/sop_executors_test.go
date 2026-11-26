package engine

import (
	"errors"
	"testing"
	"time"

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
	access.execution = domain.TelemetryContext{ResponsibleUserID: 1, ExecutionPinned: true, WorkspaceRunID: "conversation"}
	if got := access.defaults().Executor; got != "codebuddy" {
		t.Fatalf("executor for a borrowed platform workspace = %s, want codebuddy", got)
	}
}

func TestSharedRunnerSOPUsesProjectDefaultAndResponsibleUserCredentials(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "shared-compute", Name: "Shared compute", OwnerID: 1,
		Members: []domain.ProjectMember{{UserID: 42, Role: "member"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "unshared-compute", Name: "Unshared compute", OwnerID: 42}); err != nil {
		t.Fatal(err)
	}
	runner := domain.Runner{ID: "shared", OwnerUserID: 42, Status: domain.RunnerStatusOnline,
		Runtimes: []domain.RunnerRuntime{{Provider: "codex", AuthMode: domain.RuntimeAuthLocal, Status: domain.RuntimeStatusOnline}}}
	if err := st.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRunnerSharedProjects(runner.ID, 42, []string{"shared-compute"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectDefaultRunner(1, "shared-compute", runner.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDefaultRunner(42, runner.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	e := &Engine{repo: st}
	for _, tc := range []struct {
		projectID, runnerID, mode string
	}{
		{"", runner.ID, domain.ExecutionModeRunner},
		{"shared-compute", runner.ID, domain.ExecutionModeRunner},
		{"unshared-compute", "", domain.ExecutionModeCloud},
	} {
		ownerExecution := domain.TelemetryContext{ProjectID: tc.projectID, ResponsibleUserID: 42}
		e.selectSOPExecutionTarget(&ownerExecution, false)
		if ownerExecution.ExecutionRunnerID != tc.runnerID || ownerExecution.ExecutionMode != tc.mode {
			t.Fatalf("owner target for %q = %+v, want %s %q", tc.projectID, ownerExecution, tc.mode, tc.runnerID)
		}
	}
	execution := domain.TelemetryContext{ProjectID: "shared-compute", ResponsibleUserID: 1}
	e.selectSOPExecutionTarget(&execution, false)
	if execution.ExecutionRunnerID != runner.ID || execution.ExecutionMode != domain.ExecutionModeRunner {
		t.Fatalf("project target = %+v", execution)
	}
	if e.sopExecutorAccess(execution).configured("codex") {
		t.Fatal("a project member inherited the machine owner's local login")
	}
	if _, err := st.UpdateUserCredentials(1, repo.UserCredentialUpdate{CodexAPIKey: "member-key"}); err != nil {
		t.Fatal(err)
	}
	if !e.sopExecutorAccess(execution).configured("codex") {
		t.Fatal("the responsible user's personal key is unavailable on the shared machine")
	}
	setMembers := func(t *testing.T, members []domain.ProjectMember) {
		t.Helper()
		project, ok := st.Project("shared-compute")
		if !ok {
			t.Fatal("shared-compute project not found")
		}
		project.Members = members
		if err := st.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	checkRevocation := func(t *testing.T) {
		t.Helper()
		if got := e.defaultExecutionRunner(domain.Run{ExecutionContext: execution}); got != runner.ID {
			t.Fatalf("existing run lost its frozen machine: %q", got)
		}
		for _, userID := range []int64{1, 42} {
			next := domain.TelemetryContext{ProjectID: "shared-compute", ResponsibleUserID: userID}
			e.selectSOPExecutionTarget(&next, false)
			if next.ExecutionRunnerID != "" || next.ExecutionMode != domain.ExecutionModeCloud {
				t.Fatalf("new run used revoked machine: %+v", next)
			}
		}
	}
	for _, tc := range []struct {
		name    string
		members []domain.ProjectMember
	}{
		{"owner becomes viewer", []domain.ProjectMember{{UserID: 42, Role: "viewer"}}},
		{"owner removed", []domain.ProjectMember{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setMembers(t, tc.members)
			checkRevocation(t)
		})
	}
	t.Run("owner withdraws grant", func(t *testing.T) {
		setMembers(t, []domain.ProjectMember{{UserID: 42, Role: "member"}})
		if err := st.SetRunnerSharedProjects(runner.ID, 42, nil); err != nil {
			t.Fatal(err)
		}
		checkRevocation(t)
	})
}

func TestSOPDefaultSnapshotFollowsRunnerAuthMode(t *testing.T) {
	for _, kind := range []string{"codex", "claude-code"} {
		t.Run(kind, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			if err := st.PutUser(domain.User{ID: 42, LoginName: "member"}); err != nil {
				t.Fatal(err)
			}
			project := domain.Project{ID: "sop-auth", Name: "SOP auth", OwnerID: 1,
				Members: []domain.ProjectMember{{UserID: 42, Role: "member"}}}
			if err := st.PutProject(project); err != nil {
				t.Fatal(err)
			}
			runner := domain.Runner{ID: "auth-runner", OwnerUserID: 1, Status: domain.RunnerStatusOnline,
				Runtimes: []domain.RunnerRuntime{{Provider: kind, AuthMode: domain.RuntimeAuthLocal, Status: domain.RuntimeStatusOnline}}}
			if err := st.PutRunner(runner); err != nil {
				t.Fatal(err)
			}
			if err := st.SetRunnerSharedProjects(runner.ID, 1, []string{project.ID}); err != nil {
				t.Fatal(err)
			}
			ex, _ := repo.BuiltInExecutorForKind(kind)
			personal := domain.AssistantDefaults{Executor: kind, Model: "owner-gateway", Effort: "high"}
			member := domain.AssistantDefaults{Executor: kind, Model: "member-gateway", Effort: "high"}
			for _, user := range []struct {
				id       int64
				defaults *domain.AssistantDefaults
			}{{1, &personal}, {42, &member}} {
				if err := st.SetProjectDefaultRunner(user.id, project.ID, runner.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := st.UpdateUserCredentials(user.id, repo.UserCredentialUpdate{
					CodexAPIKey: "codex-fixture-key", ClaudeCodeAPIKey: "claude-fixture-key", AssistantDefaults: user.defaults,
				}); err != nil {
					t.Fatal(err)
				}
			}
			for _, role := range []domain.AgentDefinition{
				{ID: "inherited", Name: "Inherited", ProjectID: project.ID},
				{ID: "pinned", Name: "Pinned", ProjectID: project.ID, ExecutorID: ex.ID, Model: "workflow-model", Effort: "medium"},
			} {
				if err := st.PutAgent(role); err != nil {
					t.Fatal(err)
				}
			}
			workflow := domain.WorkflowDefinition{ID: "auth-workflow", Name: "Auth workflow", ProjectID: project.ID, SandboxProfileID: "test-native",
				Nodes: []domain.WorkflowNode{
					{ID: "inherited", Type: "agent", AgentID: "inherited"},
					{ID: "pinned", Type: "agent", AgentID: "pinned"},
				},
			}
			if err := st.PutWorkflow(workflow); err != nil {
				t.Fatal(err)
			}
			e := &Engine{repo: st}
			check := func(label string, userID int64, model, effort string) {
				t.Helper()
				run, err := e.StartWithExecutionContext(workflow.ID, "", domain.RepositoryContext{},
					domain.TelemetryContext{ProjectID: project.ID, ResponsibleUserID: userID})
				if err != nil {
					t.Fatalf("%s: %v", label, err)
				}
				want := domain.AssistantDefaults{Executor: ex.ID, Model: model, Effort: effort}
				defaults := run.WorkflowSnapshot.ExecutorDefaults
				if run.ExecutionContext.ExecutionRunnerID != runner.ID || len(defaults) != 1 || defaults["inherited"] != want {
					t.Errorf("%s: runner %q, defaults %+v, want runner %q and %+v", label, run.ExecutionContext.ExecutionRunnerID, defaults, runner.ID, want)
				}
				node := workflow.Nodes[1]
				role, _ := workflowAgent(st, run.WorkflowSnapshot, node.AgentID)
				pinned, err := workflowNodeExecutor(st, run.WorkflowSnapshot, node, role)
				if err != nil || pinned.Model != "workflow-model" || pinned.Effort != "medium" {
					t.Errorf("%s: pinned role executor %+v, %v", label, pinned, err)
				}
			}

			saveAccountModel := func(model domain.CLIModel) {
				t.Helper()
				if err := st.SetRunnerLoginModels(runner.ID, 1, kind, []domain.CLIModel{{ID: "default"}, model, {ID: "second-account-model"}}); err != nil {
					t.Fatal(err)
				}
			}
			model := domain.CLIModel{ID: "machine-account", ReasoningEfforts: &[]string{"low"}, DiscoveredReasoningEfforts: &[]string{"medium"}}
			saveAccountModel(model)
			check("account login", 1, model.ID, "low")
			check("shared machine uses member key", 42, member.Model, member.Effort)
			model.ReasoningEfforts = &[]string{}
			saveAccountModel(model)
			check("model uses native effort", 1, model.ID, "")
			model.ReasoningEfforts = nil
			saveAccountModel(model)
			check("discovered effort", 1, model.ID, "medium")
			if err := st.SetRunnerLoginModels(runner.ID, 1, kind, nil); err != nil {
				t.Fatal(err)
			}
			check("empty account catalog", 1, "", "")
			if err := st.SetRunnerRuntimeAuthMode(runner.ID, kind, domain.RuntimeAuthPersonal, time.Now()); err != nil {
				t.Fatal(err)
			}
			check("owner API key", 1, personal.Model, personal.Effort)
		})
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

// Pickers accept the credentials a run on the default machine uses, but leave
// the machine's CLI support to the run-time check.
func TestExecutorCredentialsUseTheDefaultRunnerLogin(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutUser(domain.User{LoginName: "picker", AgentCLICredentials: map[string]domain.AgentCLICredential{"kimi": {APIKey: "fixture-key"}}}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("picker")
	machine := domain.Runner{ID: "picker-machine", OwnerUserID: user.ID, Status: domain.RunnerStatusOnline, Runtimes: []domain.RunnerRuntime{
		{Provider: "codex", AuthMode: domain.RuntimeAuthLocal, Status: domain.RuntimeStatusOnline},
		{Provider: "claude-code", AuthMode: domain.RuntimeAuthPersonal, Status: domain.RuntimeStatusOnline},
	}}
	if err := st.PutRunner(machine); err != nil {
		t.Fatal(err)
	}
	hasCredential := ExecutorCredentials(st, user.ID, "")
	if hasCredential("codex") {
		t.Fatal("machine login counted without a default machine")
	}
	if err := st.SetDefaultRunner(user.ID, machine.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	hasCredential = ExecutorCredentials(st, user.ID, "")
	for kind, want := range map[string]bool{"codex": true, "kimi": true, "claude-code": false, "codebuddy": false} {
		if got := hasCredential(kind); got != want {
			t.Errorf("%s credential = %t, want %t", kind, got, want)
		}
	}
	if newSOPExecutorAccess(st, domain.TelemetryContext{ResponsibleUserID: user.ID, ExecutionRunnerID: machine.ID}).configured("kimi") {
		t.Fatal("a run must still require the machine to provide the CLI")
	}
}

// A project SOP runs on the user's project default machine, so the picker
// must resolve the same machine instead of only the personal default.
func TestExecutorCredentialsFollowTheProjectDefaultRunner(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{ID: 42, LoginName: "project-picker"}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []domain.Project{
		{ID: "picker-shared", Name: "Picker shared", OwnerID: 42},
		{ID: "picker-other", Name: "Picker other", OwnerID: 42},
	} {
		if err := st.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	projectMachine := domain.Runner{ID: "picker-project-machine", OwnerUserID: 42, Status: domain.RunnerStatusOnline,
		Runtimes: []domain.RunnerRuntime{{Provider: "codex", AuthMode: domain.RuntimeAuthLocal, Status: domain.RuntimeStatusOnline}}}
	personalMachine := domain.Runner{ID: "picker-personal-machine", OwnerUserID: 42, Status: domain.RunnerStatusOnline,
		Runtimes: []domain.RunnerRuntime{{Provider: "claude-code", AuthMode: domain.RuntimeAuthLocal, Status: domain.RuntimeStatusOnline}}}
	for _, machine := range []domain.Runner{projectMachine, personalMachine} {
		if err := st.PutRunner(machine); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetRunnerSharedProjects(projectMachine.ID, 42, []string{"picker-shared"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectDefaultRunner(42, "picker-shared", projectMachine.ID); err != nil {
		t.Fatal(err)
	}
	check := func(projectID string, want map[string]bool) {
		t.Helper()
		execution := domain.TelemetryContext{ProjectID: projectID, ResponsibleUserID: 42}
		(&Engine{repo: st}).selectSOPExecutionTarget(&execution, true)
		hasCredential := ExecutorCredentials(st, 42, projectID)
		for kind, expected := range want {
			if got := hasCredential(kind); got != expected {
				t.Errorf("project %q %s credential = %t, want %t (run machine %q)", projectID, kind, got, expected, execution.ExecutionRunnerID)
			}
		}
	}
	// Only the project default is set: a run in the project uses its login.
	check("picker-shared", map[string]bool{"codex": true, "claude-code": false})
	check("", map[string]bool{"codex": false, "claude-code": false})

	// The personal default is not shared with either project, so a project run
	// does not use it, while a personal SOP does.
	if err := st.SetDefaultRunner(42, personalMachine.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	check("picker-shared", map[string]bool{"codex": true, "claude-code": false})
	check("picker-other", map[string]bool{"codex": false, "claude-code": false})
	check("", map[string]bool{"codex": false, "claude-code": true})
}

// A user with no default machine runs a project SOP on a cloud pool runner,
// using that runner's account login. The picker must reach the same answer,
// and must not depend on which pool runner a random pick would choose.
func TestExecutorCredentialsFollowTheCloudPool(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{ID: 42, LoginName: "pool-picker"}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []domain.Project{
		{ID: "pool-served", Name: "Pool served", OwnerID: 42},
		{ID: "pool-skipped", Name: "Pool skipped", OwnerID: 42},
	} {
		if err := st.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	login := func(provider string) domain.RunnerRuntime {
		return domain.RunnerRuntime{Provider: provider, AuthMode: domain.RuntimeAuthLocal, Status: domain.RuntimeStatusOnline}
	}
	// Pool runners must be new enough to run a SOP end to end.
	const poolVersion = "99.0.0"
	for _, machine := range []domain.Runner{
		{ID: "pool-codex", Status: domain.RunnerStatusOnline, RunnerVersion: poolVersion, Labels: []string{"pool"},
			Runtimes: []domain.RunnerRuntime{login("codex")}},
		{ID: "pool-claude", Status: domain.RunnerStatusOnline, RunnerVersion: poolVersion, Labels: []string{"pool"},
			Runtimes: []domain.RunnerRuntime{login("claude-code")}},
		// Not eligible for pool work: offline, owned by a user, or unlabelled.
		{ID: "pool-offline", Status: domain.RunnerStatusOffline, RunnerVersion: poolVersion, Labels: []string{"pool"},
			Runtimes: []domain.RunnerRuntime{login("cursor")}},
		{ID: "pool-owned", OwnerUserID: 7, Status: domain.RunnerStatusOnline, RunnerVersion: poolVersion, Labels: []string{"pool"},
			Runtimes: []domain.RunnerRuntime{login("cursor")}},
		{ID: "unlabelled", Status: domain.RunnerStatusOnline, RunnerVersion: poolVersion,
			Runtimes: []domain.RunnerRuntime{login("cursor")}},
	} {
		if err := st.PutRunner(machine); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{repo: st}
	check := func(name, projectID string, want map[string]bool) {
		t.Helper()
		hasCredential := e.ExecutorCredentials(42, projectID)
		for kind, expected := range want {
			if got := hasCredential(kind); got != expected {
				t.Errorf("%s: project %q %s credential = %t, want %t", name, projectID, kind, got, expected)
			}
		}
	}
	none := map[string]bool{"codex": false, "claude-code": false, "cursor": false}
	check("pool off", "pool-served", none)

	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool", Projects: []string{"pool-served"}})
	check("pool on", "pool-served", map[string]bool{"codex": true, "claude-code": true, "cursor": false})
	check("project outside the pool", "pool-skipped", none)
	check("personal SOP outside the pool", "", none)

	// Whatever pool runner a run picks, its login matches a CLI the picker offered.
	for i := 0; i < 20; i++ {
		execution := domain.TelemetryContext{ProjectID: "pool-served", ResponsibleUserID: 42}
		e.selectSOPExecutionTarget(&execution, true)
		if execution.ExecutionPool != domain.ExecutionModeCloud || execution.ExecutionRunnerID == "" {
			t.Fatalf("run did not use the pool: %+v", execution)
		}
		access := e.sopExecutorAccess(execution)
		picker := e.ExecutorCredentials(42, "pool-served")
		for _, kind := range []string{"codex", "claude-code"} {
			if access.hasCredential(kind) && !picker(kind) {
				t.Fatalf("run on %s can use %s but the picker hides it", execution.ExecutionRunnerID, kind)
			}
		}
	}

	// A default machine takes precedence over the pool, as it does at run time.
	own := domain.Runner{ID: "own-machine", OwnerUserID: 42, Status: domain.RunnerStatusOnline,
		Runtimes: []domain.RunnerRuntime{login("claude-code")}}
	if err := st.PutRunner(own); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDefaultRunner(42, own.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool", Projects: []string{"*"}})
	check("personal default", "", map[string]bool{"codex": false, "claude-code": true})
	execution := domain.TelemetryContext{ResponsibleUserID: 42}
	e.selectSOPExecutionTarget(&execution, true)
	if execution.ExecutionRunnerID != own.ID {
		t.Fatalf("personal SOP ran on %q, want the default machine", execution.ExecutionRunnerID)
	}

	// Pool required with no online runner: the run waits for one, so only
	// credentials that do not depend on a machine count.
	if err := st.SetDefaultRunner(42, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pool-codex", "pool-claude"} {
		machine, _ := st.Runner(id)
		machine.Status = domain.RunnerStatusOffline
		if err := st.PutRunner(machine); err != nil {
			t.Fatal(err)
		}
	}
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool", Projects: []string{"*"}, Required: true})
	check("pool empty", "pool-served", none)
}

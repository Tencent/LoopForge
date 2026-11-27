package engine

import (
	"context"
	"slices"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestAutomaticSOPSandboxRunsSelectedToolchain(t *testing.T) {
	if !runtime.NativeSandboxAvailable(domain.SandboxDriverNative) {
		t.Skip("native sandbox unavailable")
	}
	e, store := retryEngine(t)
	profile := domain.SandboxProfile{
		ID: "project-toolchain", Name: "Project toolchain", ProjectID: "platform", Driver: domain.SandboxDriverNative,
		Environment: map[string]string{"SOP_TOOLCHAIN_TEST": "selected"},
	}
	if err := store.PutSandbox(profile); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "run-with-selected-tools", Name: "Run with selected tools", ProjectID: "platform", AutoSelectSandbox: true,
		Nodes: []domain.WorkflowNode{{ID: "verify", Name: "Verify", Type: "script", Script: `test "$SOP_TOOLCHAIN_TEST" = selected`}}}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e.SelectSOPSandbox = func(context.Context, SOPSandboxSelection) string { return profile.ID }
	run, err := e.Start(wf.ID, "Verify project toolchain")
	if err != nil {
		t.Fatal(err)
	}
	finished := waitRun(t, store, run.ID, 8*time.Second, func(r domain.Run) bool { return domain.TerminalRunStatus(r.Status) })
	if finished.Status != "succeeded" || finished.WorkflowSnapshot.SandboxProfileID != profile.ID {
		t.Fatalf("run state=%s, chosen profile=%s, node=%+v", finished.Status, finished.WorkflowSnapshot.SandboxProfileID, finished.Nodes["verify"])
	}
}

func TestAutomaticSOPSandboxUsesCompatibleCloudRunner(t *testing.T) {
	e, store := retryEngine(t)
	const label = "pool=sop"
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: label, Projects: []string{"platform"}, Required: true})
	docker := domain.Runner{ID: "docker", OS: "linux", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionGitSSH, Labels: []string{label}}
	native := docker
	native.ID, native.NativeSandboxAvailable = "native", true
	old := native
	old.ID, old.RunnerVersion = "old-native", domain.RunnerVersionIntegrate
	for _, runner := range []domain.Runner{docker, native, old} {
		if err := store.PutRunner(runner); err != nil {
			t.Fatal(err)
		}
	}
	const privilegedID = "privileged-docker"
	if err := store.PutSandbox(domain.SandboxProfile{
		ID: privilegedID, Name: "Privileged Docker", ProjectID: "platform", Driver: domain.SandboxDriverDocker,
		Privileged: true, Dockerfile: &domain.SandboxDockerfile{Content: "FROM alpine"},
	}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ProjectID: "platform", AutoSelectSandbox: true}
	repositories := []domain.RepositoryContext{{Provider: domain.RepositoryProviderSSH}}
	for _, tc := range []struct {
		name, initialRunner, wantSandbox, wantRunner string
		pinned, nativeGoesOffline                    bool
	}{
		{name: "unbound", wantSandbox: repo.TestNativeSandboxID, wantRunner: native.ID},
		{name: "assigned docker runner", initialRunner: docker.ID, wantSandbox: repo.TestNativeSandboxID, wantRunner: native.ID},
		{name: "pinned", initialRunner: docker.ID, pinned: true, wantSandbox: repo.DefaultSandboxProfileID, wantRunner: docker.ID},
		{name: "native runner disappears during selection", initialRunner: docker.ID, nativeGoesOffline: true, wantSandbox: repo.DefaultSandboxProfileID, wantRunner: docker.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ec := domain.TelemetryContext{ProjectID: "platform", ExecutionPool: domain.ExecutionModeCloud, ExecutionRunnerID: tc.initialRunner, ExecutionPinned: tc.pinned}
			called := false
			e.SelectSOPSandbox = func(_ context.Context, selection SOPSandboxSelection) string {
				called = true
				if slices.ContainsFunc(selection.Candidates, func(c SOPSandboxCandidate) bool { return c.ID == privilegedID }) {
					t.Fatalf("privileged sandbox offered for automatic selection: %+v", selection.Candidates)
				}
				offersNative := slices.ContainsFunc(selection.Candidates, func(c SOPSandboxCandidate) bool { return c.ID == repo.TestNativeSandboxID })
				if offersNative == tc.pinned {
					t.Fatalf("unexpected candidates: %+v", selection.Candidates)
				}
				if tc.nativeGoesOffline {
					offline := native
					offline.Status = domain.RunnerStatusOffline
					if err := store.PutRunner(offline); err != nil {
						t.Fatal(err)
					}
				}
				return repo.TestNativeSandboxID
			}
			sandbox, _, err := e.selectSOPSandbox(context.Background(), wf, &ec, repositories, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if (!tc.pinned && !called) || sandbox.ID != tc.wantSandbox || ec.ExecutionRunnerID != tc.wantRunner {
				t.Fatalf("called selector=%v, sandbox=%q, runner=%q; want sandbox=%q, runner=%q", called, sandbox.ID, ec.ExecutionRunnerID, tc.wantSandbox, tc.wantRunner)
			}
		})
	}
}

func TestAutomaticSOPSandboxFallbackAndFrozenRefresh(t *testing.T) {
	e, store := retryEngine(t)
	e.ConfigureExecution("api-only", "test", 30*time.Second, false)
	const projectID = "platform"
	profile := domain.SandboxProfile{
		ID: "conversation-python", Name: "Existing Python", ProjectID: projectID, Driver: domain.SandboxDriverDocker,
		Dockerfile: &domain.SandboxDockerfile{Content: "FROM python:3.12"},
	}
	if err := store.PutSandbox(profile); err != nil {
		t.Fatal(err)
	}
	foreign := profile
	foreign.ID, foreign.ProjectID = "private-foreign", "other-project"
	if err := store.PutSandbox(foreign); err != nil {
		t.Fatal(err)
	}
	owner, err := e.StartConversationWorkspace("Inspect existing work", domain.TelemetryContext{ProjectID: projectID}, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "automatic", Name: "Automatic", ProjectID: projectID, AutoSelectSandbox: true,
		Nodes: []domain.WorkflowNode{{ID: "check", Name: "Check", Type: "script", Script: "true"}}}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.SelectSOPSandbox = func(_ context.Context, selection SOPSandboxSelection) string {
		calls++
		// The execution process is API-only, so it cannot probe native
		// isolation on the worker that will execute the LOOP.
		if len(selection.Candidates) != 2 || selection.DefaultSandboxID != profile.ID ||
			slices.ContainsFunc(selection.Candidates, func(c SOPSandboxCandidate) bool { return c.ID == foreign.ID }) {
			t.Fatalf("unexpected candidates: %+v", selection)
		}
		return foreign.ID
	}
	run, err := e.StartWithRepositoriesContext(wf.ID, "Continue", nil, domain.TelemetryContext{ProjectID: projectID, WorkspaceRunID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if run.WorkflowSnapshot.SandboxProfileID != profile.ID || run.ExecutionContext.WorkspaceRunID != owner.ID {
		t.Fatalf("invalid choice displaced the workspace sandbox: %+v", run.WorkflowSnapshot)
	}
	run.Status, run.Nodes["check"].Status = "failed", "failed"
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	modified, _ := store.Workflow(wf.ID)
	modified.AutoSelectSandbox, modified.SandboxProfileID = false, repo.DefaultSandboxProfileID
	if err := store.PutWorkflow(modified); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSandbox(profile.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(run.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Retry(run.ID, "check", "", ""); err != nil {
		t.Fatal(err)
	}
	frozen, _ := store.RunState(run.ID)
	snapshot := frozen.WorkflowSnapshot.SandboxSnapshot
	if !frozen.WorkflowSnapshot.AutoSelectSandbox || frozen.WorkflowSnapshot.SandboxProfileID != profile.ID ||
		snapshot == nil || snapshot.Dockerfile == nil || snapshot.Dockerfile.Content != profile.Dockerfile.Content || calls != 1 {
		t.Fatalf("refresh/retry changed the selected profile: snapshot=%+v selector calls=%d", frozen.WorkflowSnapshot, calls)
	}
}

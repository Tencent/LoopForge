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
		// isolation on the worker that will execute the SOP.
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

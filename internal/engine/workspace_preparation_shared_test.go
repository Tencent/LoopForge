package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSharedWorkspacePreparationConversationUsesOwnerDirectoryAndSeparateReceipts(t *testing.T) {
	t.Parallel()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	e := New(store, runtime.NewManagerWithLimit(root, 2))
	makeRun := func(id, label, owner string) domain.Run {
		return domain.Run{ID: id, WorkflowID: id, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud, WorkspaceRunID: owner}, WorkflowSnapshot: domain.WorkflowDefinition{ID: id, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "prepare", Name: "Prepare " + label, Type: "script", RunOnWorkspaceRebuild: true, Script: "mkdir -p .agentflow; printf '" + label + "\\n' >> .agentflow/shared-preparation-count"}}}, Nodes: map[string]*domain.NodeRun{"prepare": {NodeID: "prepare", Status: "succeeded", Attempt: 1}}}
	}
	parent := makeRun("shared-parent", "parent", "")
	child := makeRun("shared-child", "child", parent.ID)
	for _, run := range []domain.Run{parent, child} {
		if err = store.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.MkdirAll(filepath.Join(root, parent.ID), 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ID, child.ID, parent.ID} {
		if state, readyErr := e.EnsureWorkspaceReady(context.Background(), id); readyErr != nil || state.Status != "ready" {
			t.Fatal(id, state, readyErr)
		}
	}
	e.runEnvMu.Lock()
	parentEnv, childEnv := e.runEnvs[parent.ID], e.runEnvs[child.ID]
	e.runEnvMu.Unlock()
	if childEnv == nil || parentEnv == nil || childEnv.Workspace() != parentEnv.Workspace() || childEnv.Workspace() != filepath.Join(root, parent.ID) || childEnv.ID() == parentEnv.ID() {
		t.Fatal("shared checkout must retain independent sandbox identity")
	}
	if _, err = os.Stat(filepath.Join(root, child.ID)); !os.IsNotExist(err) {
		t.Fatal("child acquired a separate workspace directory", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, parent.ID, ".agentflow", "shared-preparation-count"))
	if err != nil || string(raw) != "parent\nchild\n" {
		t.Fatal("child receipt overwrote the parent's receipt", string(raw), err)
	}
	if err = e.releaseRunEnvironment(child.ID, false); err != nil {
		t.Fatal(err)
	}
	state, err := e.WorkspacePreparationStatus(context.Background(), child.ID)
	if err != nil || state.Status != "unprepared" {
		t.Fatal("missing child environment must not read parent's receipt", state, err)
	}
	state, err = e.WorkspacePreparationStatus(context.Background(), parent.ID)
	if err != nil || state.Status != "ready" {
		t.Fatal("child release changed parent readiness", state, err)
	}
}

func TestSharedWorkspaceFlaggedChildDoesNotRepeatParentScript(t *testing.T) {
	t.Parallel()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	child := domain.WorkflowDefinition{ID: "child-rebuild-script", Name: "Child", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "prepare", Name: "Child prepare", Type: "script", ScriptLanguage: "shell", RunOnWorkspaceRebuild: true, MaxAttempts: 1, Script: "test -f parent.txt && printf 'child\\n' >> .agentflow/shared-flow-order && printf child > child.txt"}}}
	parent := domain.WorkflowDefinition{ID: "parent-rebuild-script", Name: "Parent", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "prepare", Name: "Parent prepare", Type: "script", ScriptLanguage: "shell", RunOnWorkspaceRebuild: true, MaxAttempts: 1, Script: "mkdir -p .agentflow; printf 'parent\\n' >> .agentflow/shared-flow-order; printf parent > parent.txt"},
		{ID: "sub", Name: "Child SOP", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1},
		{ID: "after", Name: "Parent resumes", Type: "script", ScriptLanguage: "shell", MaxAttempts: 1, Script: "test -f child.txt && test \"$(grep -c '^parent$' .agentflow/shared-flow-order)\" = 1 && test \"$(grep -c '^child$' .agentflow/shared-flow-order)\" = 1 && printf 'after\\n' >> .agentflow/shared-flow-order"},
		{ID: "pause", Name: "Retain checkout", Type: "approval", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{Source: "prepare", Target: "sub", When: "success"}, {Source: "sub", Target: "after", When: "success"}, {Source: "after", Target: "pause", When: "success"}}}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err = store.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	e := New(store, runtime.NewManagerWithLimit(root, 2))
	run, err := e.Start(parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, store, run.ID, "waiting")
	current, _ := store.RunState(run.ID)
	if current.Nodes["after"].Status != "succeeded" || current.Nodes["prepare"].Attempt != 1 {
		t.Fatal(current.Nodes)
	}
	childRun, ok := store.RunState(current.Nodes["sub"].ChildRunID)
	if !ok || childRun.Status != "succeeded" || workspaceRunID(childRun) != run.ID {
		t.Fatal(childRun)
	}
	if _, err = os.Stat(filepath.Join(root, childRun.ID)); !os.IsNotExist(err) {
		t.Fatal("child checkout must not be separate", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, run.ID, ".agentflow", "shared-flow-order"))
	if err != nil || strings.TrimSpace(string(raw)) != "parent\nchild\nafter" {
		t.Fatal(string(raw), err)
	}
	_ = e.Cancel(run.ID)
}

func TestSharedWorkspacePreparationRequestCarriesOwnerWithoutChangingChildIdentity(t *testing.T) {
	t.Parallel()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := New(store, runtime.NewManager(t.TempDir()))
	runner := domain.Runner{ID: "shared-runner", RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation, Status: domain.RunnerStatusOnline}
	if err = store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	child := domain.Run{ID: "child-run", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID, RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared, WorkspaceRunID: "parent-run"}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "child-flow", SandboxProfileID: "local"}, Nodes: map[string]*domain.NodeRun{}}
	request, err := e.preparedWorkspaceRequest(child, domain.RunnerWorkspaceRequest{RunID: child.ID, WorkspaceRunID: "untrusted-directory", Operation: domain.RunnerWorkspaceOperationPrepare})
	if err != nil {
		t.Fatal(err)
	}
	if request.RunID != child.ID || request.WorkspaceRunID != "parent-run" {
		t.Fatal("workspace owner replaced child identity or trusted client input", request.RunID, request.WorkspaceRunID)
	}
	runner.RunnerVersion = "0.6.0"
	if err = store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if _, err = e.preparedWorkspaceRequest(child, domain.RunnerWorkspaceRequest{RunID: child.ID, Operation: domain.RunnerWorkspaceOperationPrepare}); err == nil {
		t.Fatal("old runner could silently ignore the shared owner")
	}
}

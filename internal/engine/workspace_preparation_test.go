package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"

	"os"
	"path/filepath"
	"strings"
	"testing"
)

func preparationWorkflow() domain.WorkflowDefinition {
	return domain.WorkflowDefinition{ID: "workspace-preparation", Name: "Workspace preparation", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "env", Name: "准备环境", Type: "script", ScriptLanguage: "shell", Script: "mkdir -p .agentflow; echo run >> .agentflow/env-count; printf environment-ready", RunOnWorkspaceRebuild: true, MaxAttempts: 1},
		{ID: "index", Name: "建立索引", Type: "script", ScriptLanguage: "shell", Script: "echo run >> .agentflow/index-count; printf index-ready", RunOnWorkspaceRebuild: true, MaxAttempts: 1},
		{ID: "work", Name: "正常业务", Type: "script", ScriptLanguage: "shell", Script: "printf business-ok", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{Source: "env", Target: "index", When: "success"}, {Source: "index", Target: "work", When: "success"}}}
}
func completedPreparationRun(t *testing.T, e *Engine, id string) domain.Run {
	t.Helper()
	run, _ := e.repo.RunState(id)
	run.WorkflowSnapshot = preparationWorkflow()
	run.Nodes = map[string]*domain.NodeRun{"env": {NodeID: "env", Status: "succeeded", Attempt: 2, Output: "old env"}, "index": {NodeID: "index", Status: "succeeded", Attempt: 3, Output: "old index"}, "work": {NodeID: "work", Status: "succeeded", Attempt: 4, Output: "old business"}}
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}
func TestWorkspacePreparationNormalOrderAndFlaggedRetryAlwaysExecutes(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wf := preparationWorkflow()
	wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: "pause", Name: "暂停确认", Type: "approval", MaxAttempts: 1})
	wf.Edges = append(wf.Edges, domain.WorkflowEdge{Source: "work", Target: "pause", When: "success"})
	if err = st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, st, run.ID, "waiting")
	root := e.RunWorkspace(run.ID)
	count := func(name string) int {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, ".agentflow", name+"-count"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(raw), "run\n")
	}
	if count("env") != 1 || count("index") != 1 {
		t.Fatal("initial SOP scripts ran more than once")
	}
	if err = e.Retry(run.ID, "index", "normal retry", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, st, run.ID, "waiting")
	if count("env") != 1 || count("index") != 2 {
		t.Fatal("flagged ordinary retry must execute its script normally")
	}
	current, _ := st.RunState(run.ID)
	if current.Nodes["env"].Attempt != 1 || current.Nodes["index"].Attempt != 2 {
		t.Fatal(current.Nodes)
	}
	_ = e.Cancel(run.ID)
}
func TestWorkspacePreparationDoesNotAdvanceFutureScriptPastBusinessNode(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wf := preparationWorkflow()
	wf.Nodes[0].Script = "mkdir -p .agentflow; printf env > .agentflow/order"
	wf.Nodes[1].Script = "test \"$(cat .agentflow/order)\" = env-business; printf -- -index >> .agentflow/order"
	wf.Nodes[2].Script = "test \"$(cat .agentflow/order)\" = env; printf -- -business >> .agentflow/order"
	wf.Nodes = []domain.WorkflowNode{wf.Nodes[1], wf.Nodes[2], wf.Nodes[0], {ID: "pause", Type: "approval", MaxAttempts: 1}}
	wf.Edges = []domain.WorkflowEdge{{Source: "env", Target: "work", When: "success"}, {Source: "work", Target: "index", When: "success"}, {Source: "index", Target: "pause", When: "success"}}
	if err = st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, st, run.ID, "waiting")
	raw, err := os.ReadFile(filepath.Join(e.RunWorkspace(run.ID), ".agentflow", "order"))
	if err != nil || string(raw) != "env-business-index" {
		t.Fatal(string(raw), err)
	}
	_ = e.Cancel(run.ID)
}
func TestWorkspacePreparationRebuildPreservesHistoryAndWarmEditsDoNotReplay(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	completedPreparationRun(t, e, id)
	state, err := e.EnsureWorkspaceReady(context.Background(), id)
	if err != nil || state.Status != "ready" {
		t.Fatal(state, err)
	}
	if err = os.WriteFile(filepath.Join(e.RunWorkspace(id), "untracked.txt"), []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	e.AppendRunEvent(id, "workspace.changed", "manual edit")
	if _, err = e.EnsureWorkspaceReady(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"env", "index"} {
		raw, _ := os.ReadFile(filepath.Join(e.RunWorkspace(id), ".agentflow", name+"-count"))
		if string(raw) != "run\n" {
			t.Fatal("warm edit replayed script", name, string(raw))
		}
	}
	current, _ := e.repo.RunState(id)
	if current.Status != "succeeded" || current.Nodes["index"].Attempt != 3 || current.Nodes["index"].Output != "old index" {
		t.Fatal(current)
	}
	content, _ := os.ReadFile(filepath.Join(e.RunWorkspace(id), "README.md"))
	if string(content) != "uncommitted" {
		t.Fatal("overwritten source")
	}
}
func TestWorkspacePreparationPlanExcludesPendingCurrentAndUncheckedScripts(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run := completedPreparationRun(t, e, id)
	run.Nodes["index"].Status = "pending"
	run.WorkflowSnapshot.Nodes[0].RunOnWorkspaceRebuild = false
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if len(runtime.PreparationNodes(workspacePreparationPlan(run))) != 0 {
		t.Fatal("uncompleted or unchecked script selected")
	}
	if _, err := e.EnsureWorkspaceReady(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.RunWorkspace(id), ".agentflow", "env-count")); !os.IsNotExist(err) {
		t.Fatal("unselected script replayed")
	}
}
func TestWorkspacePreparationFailedLeavesFilesReadable(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run := completedPreparationRun(t, e, id)
	run.WorkflowSnapshot.Nodes[0].Script = "exit 17"
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	state, err := e.EnsureWorkspaceReady(context.Background(), id)
	if err == nil || state.Status != "failed" {
		t.Fatal(state, err)
	}
	current, _ := e.repo.RunState(id)
	if current.Status != "succeeded" {
		t.Fatal(current.Status)
	}
	if raw, err := os.ReadFile(filepath.Join(e.RunWorkspace(id), "README.md")); err != nil || string(raw) != "uncommitted" {
		t.Fatal(string(raw), err)
	}
}
func TestWorkspacePreparationActiveGroupReusesWithoutMutatingSource(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run := completedPreparationRun(t, e, id)
	run.Kind = domain.RunKindWorkspace
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EnsureWorkspaceReady(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	e.runEnvMu.Lock()
	env := e.runEnvs[id]
	e.runEnvMu.Unlock()
	env.activeExecutions.Store(1)
	if err := os.WriteFile(filepath.Join(env.Workspace(), "source-in-progress"), []byte("in progress"), 0600); err != nil {
		t.Fatal(err)
	}
	env.nodeMu.Lock()
	state, err := e.PrepareNodeWorkspace(context.Background(), run, env)
	env.nodeMu.Unlock()
	env.activeExecutions.Store(0)
	if err != nil || state.Status != "ready" {
		t.Fatal(state, err)
	}
	raw, _ := os.ReadFile(filepath.Join(env.Workspace(), ".agentflow", "index-count"))
	if string(raw) != "run\n" {
		t.Fatal("active scripts replayed")
	}
}
func TestWorkspacePreparationStatusAfterHandleLossDoesNotClaimReady(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	completedPreparationRun(t, e, id)
	if _, err := e.EnsureWorkspaceReady(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := e.releaseRunEnvironment(id, false); err != nil {
		t.Fatal(err)
	}
	state, err := e.WorkspacePreparationStatus(context.Background(), id)
	if err != nil || state.Status != "unprepared" {
		t.Fatal(state, err)
	}
	e.runEnvMu.Lock()
	reopened := e.runEnvs[id]
	e.runEnvMu.Unlock()
	if reopened != nil {
		t.Fatal("status created a sandbox")
	}
}

func TestWorkspacePreparationRecordFailureDoesNotRetrySuccessfulBusinessScript(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wf := domain.WorkflowDefinition{ID: "record-warning", Name: "Record warning", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "script", Name: "成功脚本", Type: "script", ScriptLanguage: "shell", RunOnWorkspaceRebuild: true, MaxAttempts: 3, Script: "printf 'run\\n' >> count.txt; mv .agentflow/workspace-preparation .agentflow/saved-preparation; ln -s saved-preparation .agentflow/workspace-preparation; printf actually-succeeded"}, {ID: "pause", Type: "approval", MaxAttempts: 1}}, Edges: []domain.WorkflowEdge{{Source: "script", Target: "pause", When: "success"}}}
	if err = st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, st, run.ID, "waiting")
	current, _ := st.RunState(run.ID)
	if current.Nodes["script"].Status != "succeeded" || current.Nodes["script"].Attempt != 1 {
		t.Fatal("successful script retried due to receipt write", current.Nodes["script"])
	}
	raw, err := os.ReadFile(filepath.Join(e.RunWorkspace(run.ID), "count.txt"))
	if err != nil || string(raw) != "run\n" {
		t.Fatal(string(raw), err)
	}
	warned := false
	for _, event := range current.Events {
		if event.Type == "workspace.preparation.record_failed" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("record failure warning missing")
	}
	_ = e.Cancel(run.ID)
}

func TestWorkspacePreparationTrustedRequestCarriesWorkspaceKind(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run := completedPreparationRun(t, e, id)
	run.Kind = domain.RunKindWorkspace
	request, err := e.preparedWorkspaceRequest(run, domain.RunnerWorkspaceRequest{RunID: id, Operation: domain.RunnerWorkspaceOperationPrepare})
	if err != nil {
		t.Fatal(err)
	}
	if request.RunKind != domain.RunKindWorkspace || request.Workflow == nil || request.Workflow.CollaborationMode != "" || request.Workflow.SOPCheckpoint != nil {
		t.Fatal("group mode lost during filtered plan projection", request)
	}
}

// While agents or a terminal hold the workspace, an IDE request reuses the
// environment they already run in, reports a trivial plan as ready, and only
// refuses when real preparation would have to run underneath them.
func TestEnsureWorkspaceReadyWhileWorkspaceBusy(t *testing.T) {
	t.Parallel()
	t.Run("reuses prepared environment", func(t *testing.T) {
		e, id := checkpointFixture(t)
		completedPreparationRun(t, e, id)
		if state, err := e.EnsureWorkspaceReady(context.Background(), id); err != nil || state.Status != "ready" {
			t.Fatal(state, err)
		}
		e.WorkspaceActive = func(domain.Run) bool { return true }
		state, err := e.EnsureWorkspaceReady(context.Background(), id)
		if err != nil || state.Status != "ready" {
			t.Fatalf("busy prepared workspace = %+v, %v", state, err)
		}
		raw, _ := os.ReadFile(filepath.Join(e.RunWorkspace(id), ".agentflow", "env-count"))
		if string(raw) != "run\n" {
			t.Fatalf("busy workspace replayed preparation: %q", raw)
		}
	})
	t.Run("trivial plan is ready", func(t *testing.T) {
		e, id := checkpointFixture(t)
		run := completedPreparationRun(t, e, id)
		for i := range run.WorkflowSnapshot.Nodes {
			run.WorkflowSnapshot.Nodes[i].RunOnWorkspaceRebuild = false
		}
		if err := e.repo.PutRun(run); err != nil {
			t.Fatal(err)
		}
		e.WorkspaceActive = func(domain.Run) bool { return true }
		state, err := e.EnsureWorkspaceReady(context.Background(), id)
		if err != nil || state.Status != "ready" || len(state.Steps) != 0 {
			t.Fatalf("busy trivial workspace = %+v, %v", state, err)
		}
		if _, err := os.Stat(filepath.Join(e.RunWorkspace(id), ".agentflow", "env-count")); !os.IsNotExist(err) {
			t.Fatal("trivial plan ran a script")
		}
	})
	t.Run("unprepared plan is refused", func(t *testing.T) {
		e, id := checkpointFixture(t)
		completedPreparationRun(t, e, id)
		e.WorkspaceActive = func(domain.Run) bool { return true }
		state, err := e.EnsureWorkspaceReady(context.Background(), id)
		if err == nil || state.Status != "unprepared" || !strings.Contains(err.Error(), "成员或终端正在使用工作区") {
			t.Fatalf("busy unprepared workspace = %+v, %v", state, err)
		}
	})
}

// A run with nothing to replay never writes a ready preparation status; the
// IDE terminal must still open in the environment agents are using.
func TestWorkspaceTerminalOpensForTrivialPlanWhileBusy(t *testing.T) {
	t.Parallel()
	e, id := checkpointFixture(t)
	run := completedPreparationRun(t, e, id)
	for i := range run.WorkflowSnapshot.Nodes {
		run.WorkflowSnapshot.Nodes[i].RunOnWorkspaceRebuild = false
	}
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.WorkspaceActive = func(domain.Run) bool { return true }
	var pool runtime.TerminalPool
	opened, err := e.HandleWorkspaceTerminal(context.Background(), id, &pool, "trivial:1", domain.WorkspaceTerminalRequest{Operation: "open"}, 0)
	if err != nil || opened.ID == "" {
		t.Fatalf("trivial busy terminal = %+v, %v", opened, err)
	}
	defer pool.CloseScope("trivial:1")
	if state := runtime.ReadWorkspacePreparationStatus(e.RunWorkspace(id), ""); state.Status == "ready" {
		t.Fatalf("precondition: trivial plan wrote a ready status %+v", state)
	}
}

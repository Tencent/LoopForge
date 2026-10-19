package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
func TestWorkspacePreparationGroupUsesSucceededFrozenSOPState(t *testing.T) {
	e, id := checkpointFixture(t)
	run := completedPreparationRun(t, e, id)
	original := run.WorkflowSnapshot
	states := run.Nodes
	run.WorkflowSnapshot.CollaborationMode = "group"
	run.WorkflowSnapshot.SOPCheckpoint = &domain.SOPCheckpoint{Workflow: original, Nodes: states}
	run.WorkflowSnapshot.Edges = nil
	run.Nodes = map[string]*domain.NodeRun{}
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EnsureWorkspaceReady(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"env", "index"} {
		raw, _ := os.ReadFile(filepath.Join(e.RunWorkspace(id), ".agentflow", name+"-count"))
		if string(raw) != "run\n" {
			t.Fatal("frozen completed script missing", name, string(raw))
		}
	}
}
func TestWorkspacePreparationFailedLeavesFilesReadable(t *testing.T) {
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
	e, id := checkpointFixture(t)
	run := completedPreparationRun(t, e, id)
	run.WorkflowSnapshot.CollaborationMode = "group"
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
func TestWorkspacePreparationRemoteContinuousWaveReusesAfterEarlierMemberFinishes(t *testing.T) {
	e, id := checkpointFixture(t)
	run := completedPreparationRun(t, e, id)
	run.WorkflowSnapshot.CollaborationMode = "group"
	run.Status = "running"
	start := time.Now().UTC().Add(-time.Minute)
	for _, state := range run.Nodes {
		state.StartedAt = start.Add(-time.Hour)
		state.FinishedAt = start.Add(-time.Hour + time.Second)
	}
	for _, nodeID := range []string{"A", "B", "C"} {
		run.WorkflowSnapshot.Nodes = append(run.WorkflowSnapshot.Nodes, domain.WorkflowNode{ID: nodeID, Type: "agent", AgentID: nodeID})
	}
	run.Nodes["A"] = &domain.NodeRun{NodeID: "A", Status: "succeeded", Attempt: 1, StartedAt: start, FinishedAt: start.Add(3 * time.Second)}
	run.Nodes["B"] = &domain.NodeRun{NodeID: "B", Status: "running", Attempt: 1, StartedAt: start.Add(2 * time.Second)}
	run.Nodes["C"] = &domain.NodeRun{NodeID: "C", Status: "running", Attempt: 1, StartedAt: start.Add(4 * time.Second)}
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
	run.ExecutionContext.ExecutionRunnerID = "workspace-runner"
	run.ExecutionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
	if err := e.repo.PutRunner(domain.Runner{ID: "workspace-runner", RunnerVersion: domain.RunnerVersionWorkspacePreparation, Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		calls++
		if request.Operation != domain.RunnerWorkspaceOperationPreparationStatus {
			t.Fatalf("reprepared active group: %s", request.Operation)
		}
		raw, _ := json.Marshal(runtime.WorkspacePreparationStatus{Status: "ready", UpdatedAt: start.Add(time.Second), Steps: []runtime.WorkspacePreparationStep{}})
		return domain.RunnerWorkspaceResponse{Payload: raw}, nil
	}
	state, err := e.PrepareNodeWorkspace(context.Background(), run, nil)
	if err != nil || state.Status != "ready" || calls != 1 {
		t.Fatal(state, err, calls)
	}
}
func TestWorkspacePreparationStatusAfterHandleLossDoesNotClaimReady(t *testing.T) {
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

func TestWorkspacePreparationTrustedRequestKeepsGroupModeOutsideFilteredPlan(t *testing.T) {
	e, id := checkpointFixture(t)
	run := completedPreparationRun(t, e, id)
	original := run.WorkflowSnapshot
	run.WorkflowSnapshot.CollaborationMode = "group"
	run.WorkflowSnapshot.SOPCheckpoint = &domain.SOPCheckpoint{Workflow: original, Nodes: run.Nodes}
	request, err := e.preparedWorkspaceRequest(run, domain.RunnerWorkspaceRequest{RunID: id, Operation: domain.RunnerWorkspaceOperationPrepare})
	if err != nil {
		t.Fatal(err)
	}
	if request.CollaborationMode != "group" || request.Workflow == nil || request.Workflow.CollaborationMode != "" || request.Workflow.SOPCheckpoint != nil {
		t.Fatal("group mode lost during filtered plan projection", request)
	}
}

package engine

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestDirectScriptNodeSuccessFailureAndResolution(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := leaseEngine(t, st, "cve-executor")

	successNode := domain.WorkflowNode{
		ID: "script", Name: "Script", Type: "script", ScriptLanguage: "shell",
		Script:          "mkdir -p out && printf ok > out/result.txt",
		ArtifactOutputs: []domain.ArtifactContract{{Name: "result", Path: "out/result.txt", MediaType: "text/plain", Required: true}},
	}
	successWF := domain.WorkflowDefinition{ID: "wf-script", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{successNode}}
	success := domain.Run{
		ID: "cve-direct-script-success", Status: "running", WorkflowID: successWF.ID, WorkflowSnapshot: successWF,
		Nodes: map[string]*domain.NodeRun{successNode.ID: {NodeID: successNode.ID, Status: "pending"}},
	}
	if err := st.PutRun(success); err != nil {
		t.Fatal(err)
	}
	if _, err := e.acquireLease(success); err != nil {
		t.Fatal(err)
	}
	e.executeNode(context.Background(), success.ID, successWF, successNode)
	got, _ := st.Run(success.ID)
	if got.Nodes[successNode.ID].Status == "pending" || got.Nodes[successNode.ID].Attempt != 1 {
		t.Fatalf("successful script=%+v artifacts=%v", got.Nodes[successNode.ID], got.Artifacts)
	}

	failedNode := domain.WorkflowNode{ID: "fail", Name: "Fail", Type: "script", ScriptLanguage: "shell", Script: "printf failure >&2; exit 7", MaxAttempts: 1}
	failedWF := domain.WorkflowDefinition{ID: "wf-fail", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{failedNode}}
	failed := domain.Run{
		ID: "cve-direct-script-failure", Status: "running", WorkflowID: failedWF.ID, WorkflowSnapshot: failedWF,
		Nodes: map[string]*domain.NodeRun{failedNode.ID: {NodeID: failedNode.ID, Status: "pending"}},
	}
	if err := st.PutRun(failed); err != nil {
		t.Fatal(err)
	}
	if _, err := e.acquireLease(failed); err != nil {
		t.Fatal(err)
	}
	e.executeNode(context.Background(), failed.ID, failedWF, failedNode)
	failedGot, _ := st.RunState(failed.ID)
	if failedGot.Nodes[failedNode.ID].Status == "pending" || failedGot.Nodes[failedNode.ID].Attempt != 1 {
		t.Fatalf("failed script=%+v", failedGot.Nodes[failedNode.ID])
	}

	agentNode := domain.WorkflowNode{ID: "agent", Name: "Agent", Type: "agent", AgentID: "missing-agent"}
	agentWF := domain.WorkflowDefinition{ID: "wf-agent", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{agentNode}}
	agentRun := domain.Run{
		ID: "cve-direct-agent-resolution", Status: "running", WorkflowID: agentWF.ID, WorkflowSnapshot: agentWF,
		Nodes: map[string]*domain.NodeRun{agentNode.ID: {NodeID: agentNode.ID, Status: "pending"}},
	}
	if err := st.PutRun(agentRun); err != nil {
		t.Fatal(err)
	}
	if _, err := e.acquireLease(agentRun); err != nil {
		t.Fatal(err)
	}
	e.executeNode(context.Background(), agentRun.ID, agentWF, agentNode)
	agentGot, _ := st.RunState(agentRun.ID)
	if agentGot.Nodes[agentNode.ID].Status == "pending" || agentGot.Nodes[agentNode.ID].Attempt != 1 {
		t.Fatalf("agent resolution=%+v", agentGot.Nodes[agentNode.ID])
	}
}

func TestWorkspaceCleanupHelpersAndDeleteValidation(t *testing.T) {
	e, st := cvEEngine(t)
	if e.RunWorkspace("missing") != "" {
		t.Fatal("nil manager returned workspace")
	}
	e.recordWorkspaceFacts("run", nil)
	e.recordWorkspaceFacts("run", &domain.RunnerTaskOutputs{})
	facts := domain.RunnerWorkspaceFacts{Frontend: true, FrontendEvidence: "package.json"}
	e.recordWorkspaceFacts("run", &domain.RunnerTaskOutputs{Facts: &facts})
	if got, ok := e.RunWorkspaceFacts("run"); !ok || !got.Frontend {
		t.Fatalf("facts=%+v ok=%v", got, ok)
	}
	if _, ok := e.RunWorkspaceFacts("missing"); ok {
		t.Fatal("missing facts found")
	}
	if WorkspaceRunID(domain.Run{ID: "child", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: "parent"}}) != "parent" {
		t.Fatal("workspace owner not resolved")
	}
	if !isLiveStatus("running") || !isLiveStatus("waiting") || isLiveStatus("failed") {
		t.Fatal("live statuses")
	}

	now := time.Now().UTC()
	for _, tc := range []struct {
		run  domain.Run
		idle bool
	}{
		{run: domain.Run{Status: "failed", UpdatedAt: now.Add(-idleFailedRunContainerTTL - time.Minute), Nodes: map[string]*domain.NodeRun{}}, idle: true},
		{run: domain.Run{Status: "failed", UpdatedAt: now, Nodes: map[string]*domain.NodeRun{}}, idle: false},
		{run: domain.Run{Status: "waiting", UpdatedAt: now.Add(-idleWaitingRunContainerTTL - time.Minute), Nodes: map[string]*domain.NodeRun{}}, idle: true},
		{run: domain.Run{Status: "waiting", UpdatedAt: now.Add(-idleWaitingRunContainerTTL - time.Minute), Nodes: map[string]*domain.NodeRun{"node": {Status: "running"}}}, idle: false},
		{run: domain.Run{Status: "succeeded", UpdatedAt: now.Add(-24 * time.Hour), Nodes: map[string]*domain.NodeRun{}}, idle: false},
		{run: domain.Run{Status: "failed", WorkspaceCleanedAt: now, UpdatedAt: now.Add(-24 * time.Hour), Nodes: map[string]*domain.NodeRun{}}, idle: false},
	} {
		if got := runContainerIdle(tc.run, now); got != tc.idle {
			t.Fatalf("idle=%v want=%v run=%+v", got, tc.idle, tc.run)
		}
	}
	if err := e.DeleteRun("missing"); err == nil {
		t.Fatal("missing run deleted")
	}
	active := domain.Run{ID: "delete-active", Status: "running", Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(active); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteRun(active.ID); err == nil {
		t.Fatal("active run deleted")
	}
	terminal := domain.Run{ID: "delete-busy", Status: "failed", Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(terminal); err != nil {
		t.Fatal(err)
	}
	e.WorkspaceActive = func(domain.Run) bool { return true }
	if err := e.DeleteRun(terminal.ID); err == nil {
		t.Fatal("busy workspace run deleted")
	}
	e.WorkspaceActive = nil
	if err := e.settleDeletedRunTasks(domain.Run{ID: "unknown-status", Status: "waiting"}); err != nil {
		t.Fatal(err)
	}
	e.cleanupRunWorkspace("missing")
	e.CancelPreviewArchive("missing")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.sandboxSweep(ctx)

	local := New(st, runtime.NewManager(t.TempDir()))
	cleanup := domain.Run{ID: "cleanup-local-more", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(cleanup); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(local.RunWorkspace(cleanup.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	local.cleanupRunWorkspace(cleanup.ID)
	cleaned, _ := st.RunState(cleanup.ID)
	if cleaned.WorkspaceCleanedAt.IsZero() {
		t.Fatalf("cleanup not recorded: %+v", cleaned)
	}
	local.cleanupWorkspace(cleaned, time.Now().UTC())

	runner := domain.Runner{ID: "cleanup-runner-more", Status: domain.RunnerStatusOnline, Runtimes: []domain.RunnerRuntime{{Provider: "terminal", Status: domain.RuntimeStatusOnline}}}
	if err := st.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	previewTask := domain.RunnerTask{ID: "preview-archive-more", RunnerID: runner.ID, Provider: "terminal", RunID: "preview-run-more", Action: domain.RunnerTaskActionArchivePreview}
	if err := st.PutRunnerTask(previewTask); err != nil {
		t.Fatal(err)
	}
	local.CancelPreviewArchive(previewTask.RunID)
	canceledTask, _ := st.RunnerTask(previewTask.ID)
	if canceledTask.Status != domain.RunnerTaskCanceled {
		t.Fatalf("preview task=%+v", canceledTask)
	}

	for _, task := range []domain.Task{
		{ID: "task-by-run-more", RunID: "settle-run-more", Status: "running"},
		{ID: "task-by-context-more", RunID: "other", Status: "running"},
		{ID: "task-unchanged-more", RunID: "settle-run-more", Status: "completed"},
		{ID: "task-unrelated-more", RunID: "other", Status: "running"},
	} {
		if err := st.PutTask(task); err != nil {
			t.Fatal(err)
		}
	}
	if err := local.settleDeletedRunTasks(domain.Run{ID: "settle-run-more", Status: "succeeded", ExecutionContext: domain.TelemetryContext{TaskID: "task-by-context-more"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"task-by-run-more", "task-by-context-more", "task-unchanged-more"} {
		task, _ := st.Task(id)
		if task.Status != "completed" {
			t.Fatalf("settled task %s=%+v", id, task)
		}
	}
	if task, _ := st.Task("task-unrelated-more"); task.Status != "running" {
		t.Fatalf("unrelated task=%+v", task)
	}
	if _, err := local.WorkspaceRestoreStatus("restore-missing-more"); err == nil {
		t.Fatal("missing restore status succeeded")
	}
	parent := domain.Run{ID: "restore-parent-more", Status: "failed", Nodes: map[string]*domain.NodeRun{}}
	child := domain.Run{ID: "restore-child-more", Status: "failed", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRun(child); err != nil {
		t.Fatal(err)
	}
	if state, err := local.WorkspaceRestoreStatus(child.ID); err != nil || state.Status != "ready" {
		t.Fatalf("shared restore status=%+v err=%v", state, err)
	}
}

func TestWorkspacePreparationDirectValidationAndRemoteStates(t *testing.T) {
	e, st := cvEEngine(t)
	if _, _, err := e.workspaceSandboxProfile(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "missing"}}); err == nil {
		t.Fatal("missing sandbox profile accepted")
	}
	snapshot := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "snapshot", Name: "Snapshot", Driver: "local"}}
	if profile, _, err := e.workspaceSandboxProfile(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{SandboxSnapshot: snapshot}}); err != nil || profile.ID != "snapshot" {
		t.Fatalf("snapshot profile=%+v err=%v", profile, err)
	}
	foreign := domain.Run{ID: "foreign", WorkspaceHost: "other", WorkspaceRoot: "/workspace"}
	if _, err := e.acquireConversationEnvironment(context.Background(), foreign); err == nil {
		t.Fatal("foreign workspace acquired")
	}
	if workspaceCanRefreshRepositories(domain.Run{ExecutionContext: domain.TelemetryContext{WorkspaceRunID: "parent"}}) {
		t.Fatal("shared workspace refresh allowed")
	}
	refreshRun := domain.Run{
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "prepare", Type: "script", RunOnWorkspaceRebuild: true}, {ID: "work"}}},
		Nodes:            map[string]*domain.NodeRun{"prepare": {Status: "succeeded"}, "work": {Status: "pending"}},
	}
	if !workspaceCanRefreshRepositories(refreshRun) {
		t.Fatal("clean preparation cannot refresh")
	}
	refreshRun.Nodes["work"].Attempt = 1
	if workspaceCanRefreshRepositories(refreshRun) {
		t.Fatal("worked repository refresh allowed")
	}
	planRun := refreshRun
	planRun.Nodes["prepare"].Status = "failed"
	plan := workspacePreparationPlan(planRun)
	if plan.CollaborationMode != "" || plan.Nodes[0].RunOnWorkspaceRebuild {
		t.Fatalf("preparation plan=%+v", plan)
	}
	if requirements, err := workspaceConversationRequirements(st, domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{Type: "agent", AgentID: "missing"}}}); err != nil || len(requirements.CLIStates) != 0 {
		t.Fatalf("conversation requirements=%+v err=%v", requirements, err)
	}
	if _, err := e.WorkspacePreparationStatus(context.Background(), "missing"); err == nil {
		t.Fatal("missing preparation status succeeded")
	}
	if _, err := e.EnsureWorkspaceReady(context.Background(), "missing"); err == nil {
		t.Fatal("missing preparation succeeded")
	}
	if _, err := e.TrustedWorkspaceRequest("missing", domain.RunnerWorkspaceRequest{}); err == nil {
		t.Fatal("missing trusted request succeeded")
	}
	if _, err := e.HandlePreparedWorkspaceTerminal(context.Background(), domain.Run{ID: "missing"}, &runtime.TerminalPool{}, "scope", domain.WorkspaceTerminalRequest{Operation: "open"}); err == nil {
		t.Fatal("unprepared terminal opened")
	}
	if state, err := e.PrepareNodeWorkspace(context.Background(), domain.Run{}, &runEnvironment{}); err != nil || state.Status != "ready" {
		t.Fatalf("empty node preparation=%+v err=%v", state, err)
	}
	if err := e.recordNodeWorkspacePreparation(context.Background(), domain.Run{}, nil, domain.WorkflowNode{}, "ignored"); err != nil {
		t.Fatal(err)
	}

	runner := cvERunner(t, e, "prep-runner-more", domain.RunnerStatusOnline, "terminal")
	runner.RunnerVersion = domain.RunnerVersionWorkspaceOwnerPreparation
	if err := st.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	remote := cvERunnerRun("prep-remote-more", runner.ID)
	remote.Status = "waiting"
	if err := st.PutRun(remote); err != nil {
		t.Fatal(err)
	}
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Payload: json.RawMessage(`{"status":"ready","steps":[]}`)}, nil
	}
	if state, err := e.WorkspacePreparationStatus(context.Background(), remote.ID); err != nil || state.Status != "ready" {
		t.Fatalf("remote ready=%+v err=%v", state, err)
	}
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Error: "remote failed"}, nil
	}
	if state, err := e.EnsureWorkspaceReady(context.Background(), remote.ID); err == nil || state.Status != "failed" {
		t.Fatalf("remote failure=%+v err=%v", state, err)
	}
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{}, nil
	}
	if _, err := e.EnsureWorkspaceReady(context.Background(), remote.ID); err == nil {
		t.Fatal("empty remote status accepted")
	}
}

func TestInterruptedRunRecoverySettlesControlAndCompletedGraphs(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := leaseEngine(t, st, "cve-recovery")
	wf := domain.WorkflowDefinition{ID: "recovery-wf", Nodes: []domain.WorkflowNode{{ID: "node"}}}
	runs := []domain.Run{
		{ID: "recover-waiting-cancel", Status: "waiting", CancelRequested: true, WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "waiting_approval"}}},
		{ID: "recover-running-cancel", Status: "running", CancelRequested: true, WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "running"}}},
		{ID: "recover-running-pause", Status: "running", PauseRequested: true, WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "running"}}},
		{ID: "recover-completed", Status: "running", WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "succeeded"}}},
	}
	for _, run := range runs {
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	_ = e.RecoverInterruptedRuns()
	for _, tc := range []struct{ id, status string }{
		{"recover-waiting-cancel", "cancelled"},
		{"recover-running-cancel", "cancelled"},
		{"recover-running-pause", "waiting"},
		{"recover-completed", "succeeded"},
	} {
		got, ok := st.RunState(tc.id)
		if !ok || got.Status != tc.status {
			t.Fatalf("%s status=%q want=%q", tc.id, got.Status, tc.status)
		}
	}
	if e.ActiveRunCount() != 0 {
		t.Fatalf("active recoveries=%d", e.ActiveRunCount())
	}
	if !e.claimRunning("claimed", func() {}) || e.claimRunning("claimed", func() {}) {
		t.Fatal("claim running idempotence")
	}
	e.clearRunning("claimed")
	unlock := e.lockNodeExec("run", "node")
	unlock()
}

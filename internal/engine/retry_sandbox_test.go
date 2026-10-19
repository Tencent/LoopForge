package engine

import (
	"context"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

type recordingSandbox struct {
	runtime.Sandbox
	workspace string
	closed    bool
}

func (s *recordingSandbox) ID() string        { return "sandbox" }
func (s *recordingSandbox) Workspace() string { return s.workspace }
func (s *recordingSandbox) Close(context.Context) error {
	s.closed = true
	return nil
}

func retryableRun(t *testing.T, store *repo.Store, id string) domain.Run {
	t.Helper()
	workflow := domain.WorkflowDefinition{
		ID: "wf", Version: 1,
		Nodes: []domain.WorkflowNode{{ID: "a"}, {ID: "b"}},
		Edges: []domain.WorkflowEdge{{Source: "a", Target: "b"}},
	}
	run := domain.Run{
		ID: id, Status: "failed", WorkflowSnapshot: workflow,
		Nodes: map[string]*domain.NodeRun{
			"a": {NodeID: "a", Status: "succeeded"},
			"b": {NodeID: "b", Status: "failed"},
		},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}

func retryEngine(t *testing.T) (*Engine, *repo.Store) {
	t.Helper()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return New(store, runtime.NewManager(t.TempDir())), store
}

// 从入口节点重跑等于整条 SOP 重来，旧容器的可写层里全是上一次的残留，
// 而且不主动关掉的话它会一直挂到 run 最终成功为止。
func TestRetryFromEntryNodeDiscardsSandbox(t *testing.T) {
	engine, store := retryEngine(t)
	run := retryableRun(t, store, "rerun-all")
	sandbox := &recordingSandbox{workspace: engine.sandboxes.RunWorkspace(run.ID)}
	engine.runEnvs[run.ID] = &runEnvironment{sandbox: sandbox}

	if err := engine.Retry(run.ID, "a", "从头重跑", ""); err != nil {
		t.Fatal(err)
	}

	if !sandbox.closed {
		t.Fatal("full rerun must tear the previous container down")
	}
	if _, held := engine.runEnvs[run.ID]; held {
		t.Fatal("stale sandbox handle survived the rerun")
	}
}

// 需求被重新触发（而不是点重试）时会新建一个 run，旧 run 停在 failed。
// 失败的 run 为了重试一直留着工作区和容器，重新从头跑之后就再没人回收它了。
func TestSupersedeIssueRunsDiscardsReplacedSandbox(t *testing.T) {
	engine, store := retryEngine(t)
	old := domain.Run{
		ID: "run-old", Status: "failed",
		ExecutionContext: domain.TelemetryContext{IssueID: "issue-1"},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(old); err != nil {
		t.Fatal(err)
	}
	sandbox := &recordingSandbox{workspace: engine.sandboxes.RunWorkspace(old.ID)}
	engine.runEnvs[old.ID] = &runEnvironment{sandbox: sandbox}

	engine.SupersedeIssueRuns("issue-1", "run-new")

	if !sandbox.closed {
		t.Fatal("a run replaced by a fresh dispatch must release its container")
	}
	if _, held := engine.runEnvs[old.ID]; held {
		t.Fatal("superseded sandbox handle survived")
	}
	// 工作区保留，上一次的现场还要能查。
	kept, _ := store.Run(old.ID)
	if !kept.WorkspaceCleanedAt.IsZero() {
		t.Fatal("superseding must not mark the old workspace as cleaned")
	}
}

// 同一需求触发另一条 SOP，不能把这条 SOP 等着重试的容器回收掉。
func TestSupersedeIssueRunsKeepsOtherSOPRuns(t *testing.T) {
	engine, store := retryEngine(t)
	for _, run := range []domain.Run{
		{ID: "run-backend", WorkflowID: "backend", Status: "failed", ExecutionContext: domain.TelemetryContext{IssueID: "issue-1"}, Nodes: map[string]*domain.NodeRun{}},
		{ID: "run-frontend", WorkflowID: "frontend", Status: "running", ExecutionContext: domain.TelemetryContext{IssueID: "issue-1"}, Nodes: map[string]*domain.NodeRun{}},
	} {
		if err := store.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	sandbox := &recordingSandbox{workspace: engine.sandboxes.RunWorkspace("run-backend")}
	engine.runEnvs["run-backend"] = &runEnvironment{sandbox: sandbox}

	engine.SupersedeIssueRuns("issue-1", "run-frontend")

	if sandbox.closed {
		t.Fatal("dispatching another SOP released the failed SOP's retry container")
	}
}

// 失败和等待中的 run 为了重试/人工决策一直保留工作区，容器也跟着常驻。
// 闲置超过阈值后只回收容器，工作区留着，下次重试或审批时重建容器。
func TestDiscardIdleRunContainersReleasesStaleFailedAndWaitingRuns(t *testing.T) {
	engine, store := retryEngine(t)
	runs := []domain.Run{
		{ID: "run-failed", Status: "failed", Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "failed"}}},
		{ID: "run-approval", Status: "waiting", Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "waiting_approval"}}},
		{ID: "run-executing", Status: "waiting", Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "running"}}},
		{ID: "run-input", Status: "waiting", Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "pending_input"}}},
		{ID: "run-active", Status: "running", Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "running"}}},
	}
	sandboxes := map[string]*recordingSandbox{}
	for _, run := range runs {
		if err := store.PutRun(run); err != nil {
			t.Fatal(err)
		}
		sandboxes[run.ID] = &recordingSandbox{workspace: engine.sandboxes.RunWorkspace(run.ID)}
		engine.runEnvs[run.ID] = &runEnvironment{sandbox: sandboxes[run.ID]}
	}

	if n := engine.discardIdleRunContainers(context.Background(), time.Now().UTC().Add(3*time.Hour)); n != 2 {
		t.Fatalf("discarded %d containers, want 2", n)
	}
	for id, want := range map[string]bool{"run-failed": true, "run-approval": true, "run-executing": false, "run-input": false, "run-active": false} {
		if sandboxes[id].closed != want {
			t.Fatalf("%s closed=%v, want %v", id, sandboxes[id].closed, want)
		}
		if _, held := engine.runEnvs[id]; held == want {
			t.Fatalf("%s handle held=%v after discard", id, held)
		}
	}
	kept, _ := store.Run("run-failed")
	if !kept.WorkspaceCleanedAt.IsZero() {
		t.Fatal("discarding an idle container must keep the workspace for retry")
	}
}

func TestDiscardIdleRunContainersWaitsForIdleThreshold(t *testing.T) {
	engine, store := retryEngine(t)
	for _, run := range []domain.Run{
		{ID: "run-failed", Status: "failed", Nodes: map[string]*domain.NodeRun{}},
		{ID: "run-waiting", Status: "waiting", Nodes: map[string]*domain.NodeRun{}},
	} {
		if err := store.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	failed := &recordingSandbox{workspace: engine.sandboxes.RunWorkspace("run-failed")}
	waiting := &recordingSandbox{workspace: engine.sandboxes.RunWorkspace("run-waiting")}
	engine.runEnvs["run-failed"] = &runEnvironment{sandbox: failed}
	engine.runEnvs["run-waiting"] = &runEnvironment{sandbox: waiting}

	if n := engine.discardIdleRunContainers(context.Background(), time.Now().UTC().Add(10*time.Minute)); n != 0 || failed.closed || waiting.closed {
		t.Fatalf("a run that just failed must keep its container for a quick retry (n=%d)", n)
	}
	if n := engine.discardIdleRunContainers(context.Background(), time.Now().UTC().Add(time.Hour)); n != 1 || !failed.closed || waiting.closed {
		t.Fatalf("after an hour only the failed run is idle (n=%d failed=%v waiting=%v)", n, failed.closed, waiting.closed)
	}
}

// 从中断处继续要复用同一个容器，否则每次重试都要重建镜像。
func TestRetryFromInterruptedNodeKeepsSandbox(t *testing.T) {
	engine, store := retryEngine(t)
	run := retryableRun(t, store, "rerun-tail")
	sandbox := &recordingSandbox{workspace: engine.sandboxes.RunWorkspace(run.ID)}
	engine.runEnvs[run.ID] = &runEnvironment{sandbox: sandbox}

	if err := engine.Retry(run.ID, "b", "从中断处继续", ""); err != nil {
		t.Fatal(err)
	}

	if sandbox.closed {
		t.Fatal("partial retry must reuse the existing container")
	}
	if _, held := engine.runEnvs[run.ID]; !held {
		t.Fatal("partial retry dropped the sandbox handle")
	}
}

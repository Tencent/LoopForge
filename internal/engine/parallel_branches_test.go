package engine

import (
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func parallelTestEngine(t *testing.T, wf domain.WorkflowDefinition) (*Engine, *repo.Store) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	return New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), s
}

func scriptNode(id, script string) domain.WorkflowNode {
	return domain.WorkflowNode{ID: id, Name: id, Type: "script", ScriptLanguage: "shell", Script: script, MaxAttempts: 1}
}

func edge(source, target string) domain.WorkflowEdge {
	return domain.WorkflowEdge{ID: source + "-" + target, Source: source, Target: target, When: "success"}
}

func nodeEvents(r domain.Run, nodeID, eventType string) int {
	n := 0
	for _, event := range r.Events {
		if event.NodeID == nodeID && event.Type == eventType {
			n++
		}
	}
	return n
}

func overlaps(a, b *domain.NodeRun) bool {
	return a.StartedAt.Before(b.FinishedAt) && b.StartedAt.Before(a.FinishedAt)
}

// Two sleeping branches run at the same time, and a fast branch's successor
// starts without waiting for its slow sibling (no wave barrier).
func TestParallelBranchesExecuteConcurrently(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{ID: "parallel-overlap", Name: "parallel overlap", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			scriptNode("start", "true"),
			scriptNode("left", "sleep 1.5"),
			scriptNode("right", "sleep 1.5"),
			scriptNode("fast", "true"),
			scriptNode("after-fast", "true"),
			scriptNode("join", "true"),
		},
		Edges: []domain.WorkflowEdge{
			edge("start", "left"), edge("start", "right"), edge("start", "fast"),
			edge("fast", "after-fast"),
			edge("left", "join"), edge("right", "join"), edge("after-fast", "join"),
		},
	}
	e, s := parallelTestEngine(t, wf)
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	got, _ := s.Run(run.ID)
	left, right, afterFast, join := got.Nodes["left"], got.Nodes["right"], got.Nodes["after-fast"], got.Nodes["join"]
	if !overlaps(left, right) {
		t.Fatalf("parallel branches did not overlap: left %s..%s right %s..%s", left.StartedAt, left.FinishedAt, right.StartedAt, right.FinishedAt)
	}
	if !afterFast.StartedAt.Before(left.FinishedAt) || !afterFast.StartedAt.Before(right.FinishedAt) {
		t.Fatalf("fast branch successor waited for slow siblings: after-fast started %s, left finished %s, right finished %s", afterFast.StartedAt, left.FinishedAt, right.FinishedAt)
	}
	if join.StartedAt.Before(left.FinishedAt) || join.StartedAt.Before(right.FinishedAt) {
		t.Fatalf("join started before its dependencies finished")
	}
}

// A failed branch must not settle the run while a sibling is still running:
// the sibling finishes and its successor still runs before the run fails.
func TestSchedulerWaitsForRunningSiblingBeforeSettling(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{ID: "parallel-fail-wait", Name: "parallel fail wait", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			scriptNode("start", "true"),
			scriptNode("broken", "exit 3"),
			scriptNode("slow", "sleep 1"),
			scriptNode("after-slow", "true"),
		},
		Edges: []domain.WorkflowEdge{edge("start", "broken"), edge("start", "slow"), edge("slow", "after-slow")},
	}
	e, s := parallelTestEngine(t, wf)
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "failed")
	got, _ := s.Run(run.ID)
	if got.Nodes["broken"].Status != "failed" {
		t.Fatalf("broken = %+v", got.Nodes["broken"])
	}
	if got.Nodes["slow"].Status != "succeeded" || got.Nodes["after-slow"].Status != "succeeded" {
		t.Fatalf("scheduler settled under a running sibling: slow=%s after-slow=%s", got.Nodes["slow"].Status, got.Nodes["after-slow"].Status)
	}
}

// A branch waiting for approval keeps the run "running" while its sibling
// works; the run only waits once nothing is in flight.
func TestWaitingBranchDoesNotStopRunningSibling(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{ID: "parallel-wait-sibling", Name: "parallel wait sibling", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			scriptNode("start", "true"),
			{ID: "gate", Name: "gate", Type: "approval"},
			scriptNode("slow", "sleep 1"),
			scriptNode("after-slow", "true"),
		},
		Edges: []domain.WorkflowEdge{edge("start", "gate"), edge("start", "slow"), edge("slow", "after-slow")},
	}
	e, s := parallelTestEngine(t, wf)
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	mid := waitRun(t, s, run.ID, 30*time.Second, func(r domain.Run) bool {
		return r.Nodes["gate"] != nil && r.Nodes["gate"].Status == "waiting_approval" && r.Nodes["slow"] != nil && r.Nodes["slow"].Status == "running"
	})
	if mid.Status != "running" {
		t.Fatalf("run status with a running sibling = %s", mid.Status)
	}
	waitFor(t, s, run.ID, "waiting")
	got, _ := s.Run(run.ID)
	if got.Nodes["after-slow"].Status != "succeeded" {
		t.Fatalf("after-slow = %s", got.Nodes["after-slow"].Status)
	}
	if err := e.Approve(run.ID, "gate", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
}

// Rework from one branch resets a sibling that is still running. The stale
// execution is cancelled, never commits, and the sibling runs exactly once
// more as a new attempt.
func TestReworkResetsRunningSiblingWithoutDoubleCommit(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{ID: "parallel-rework", Name: "parallel rework", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			scriptNode("start", "true"),
			{ID: "gate", Name: "gate", Type: "approval", RejectTo: "start", MaxReworks: 3},
			scriptNode("slow", "sleep 2"),
		},
		Edges: []domain.WorkflowEdge{edge("start", "gate"), edge("start", "slow")},
	}
	e, s := parallelTestEngine(t, wf)
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, run.ID, 30*time.Second, func(r domain.Run) bool {
		return r.Nodes["gate"] != nil && r.Nodes["gate"].Status == "waiting_approval" && r.Nodes["slow"] != nil && r.Nodes["slow"].Status == "running"
	})
	if err := e.Reject(run.ID, domain.RejectRequest{NodeID: "gate", TargetID: "start", Reason: "redo"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "waiting")
	got, _ := s.Run(run.ID)
	if got.Nodes["slow"].Status != "succeeded" {
		t.Fatalf("slow = %+v", got.Nodes["slow"])
	}
	if n := nodeEvents(got, "slow", "node.succeeded"); n != 1 {
		t.Fatalf("slow committed %d successes; the reset execution must not commit", n)
	}
	if got.Nodes["slow"].Attempt != 2 {
		t.Fatalf("slow attempt = %d, want 2 (one abandoned, one rerun)", got.Nodes["slow"].Attempt)
	}
	if n := nodeEvents(got, "start", "node.succeeded"); n != 2 {
		t.Fatalf("start succeeded %d times, want 2", n)
	}
	if err := e.Approve(run.ID, "gate", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
}

// Pausing parallel branches and continuing at once resumes both; no node is
// committed twice and the run completes.
func TestPauseThenImmediateResumeParallelBranches(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{ID: "parallel-pause", Name: "parallel pause", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			scriptNode("start", "true"),
			scriptNode("left", "sleep 1"),
			scriptNode("right", "sleep 1"),
			scriptNode("join", "true"),
		},
		Edges: []domain.WorkflowEdge{edge("start", "left"), edge("start", "right"), edge("left", "join"), edge("right", "join")},
	}
	e, s := parallelTestEngine(t, wf)
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, run.ID, 30*time.Second, func(r domain.Run) bool {
		return r.Nodes["left"] != nil && r.Nodes["left"].Status == "running" && r.Nodes["right"] != nil && r.Nodes["right"].Status == "running"
	})
	if err := e.Pause(run.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "waiting")
	if err := e.ResumePaused(run.ID, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	got, _ := s.Run(run.ID)
	for _, id := range []string{"left", "right", "join"} {
		if n := nodeEvents(got, id, "node.succeeded"); n != 1 {
			t.Fatalf("%s committed %d successes", id, n)
		}
	}
}

func TestKeyedLocksSerializeOnlySameKey(t *testing.T) {
	t.Parallel()
	var locks keyedLocks
	releaseA := locks.lock("a")
	acquiredB := make(chan struct{})
	go func() {
		release := locks.lock("b")
		close(acquiredB)
		release()
	}()
	select {
	case <-acquiredB:
	case <-time.After(2 * time.Second):
		t.Fatal("different key blocked")
	}
	acquiredA := make(chan struct{})
	go func() {
		release := locks.lock("a")
		close(acquiredA)
		release()
	}()
	select {
	case <-acquiredA:
		t.Fatal("same key not serialized")
	case <-time.After(100 * time.Millisecond):
	}
	releaseA()
	select {
	case <-acquiredA:
	case <-time.After(2 * time.Second):
		t.Fatal("same key never released")
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.locks) != 0 {
		t.Fatalf("lock entries leaked: %d", len(locks.locks))
	}
}

func TestNodeControlKeyIsolatesChildSOP(t *testing.T) {
	t.Parallel()
	parent := domain.Run{ID: "run-parent"}
	child := domain.Run{ID: "run-child", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: "run-parent"}}
	if nodeControlKey(parent, "dev") != "dev" {
		t.Fatal("owner keys must stay unchanged")
	}
	if gateResultPath(nodeControlKey(parent, "dev"), 1) == gateResultPath(nodeControlKey(child, "dev"), 1) {
		t.Fatal("child SOP gate result path collides with the parent's")
	}
	artifact := domain.Artifact{NodeID: "dev", Name: "spec", MediaType: "text/markdown"}
	if artifactInputRelPath(parent, artifact) == artifactInputRelPath(child, artifact) {
		t.Fatal("child SOP input path collides with the parent's")
	}
}

// Cancelling during a parallel stage stops both branches. Replaying one of
// them must resume the other too, and the run must settle instead of staying
// "running" with a cancelled branch nobody schedules.
func TestRetryAfterCancelResumesCancelledParallelSibling(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{ID: "parallel-cancel-retry", Name: "parallel cancel retry", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			scriptNode("start", "true"),
			scriptNode("left", "sleep 1"),
			scriptNode("right", "sleep 1"),
			scriptNode("join", "true"),
		},
		Edges: []domain.WorkflowEdge{edge("start", "left"), edge("start", "right"), edge("left", "join"), edge("right", "join")},
	}
	e, s := parallelTestEngine(t, wf)
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, run.ID, 30*time.Second, func(r domain.Run) bool {
		return r.Nodes["left"] != nil && r.Nodes["left"].Status == "running" && r.Nodes["right"] != nil && r.Nodes["right"].Status == "running"
	})
	if err := e.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitRun(t, s, run.ID, 30*time.Second, func(r domain.Run) bool { return r.Status == "cancelled" && !hasRunningNode(r) })
	if cancelled.Nodes["left"].Status != "cancelled" || cancelled.Nodes["right"].Status != "cancelled" {
		t.Fatalf("cancel left left=%s right=%s", cancelled.Nodes["left"].Status, cancelled.Nodes["right"].Status)
	}
	if err := e.Retry(run.ID, "left", "", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	got, _ := s.Run(run.ID)
	for _, id := range []string{"left", "right", "join"} {
		if got.Nodes[id].Status != "succeeded" {
			t.Fatalf("%s = %s", id, got.Nodes[id].Status)
		}
	}
	if n := nodeEvents(got, "start", "node.succeeded"); n != 1 {
		t.Fatalf("start reran %d times; replaying a branch must not rerun its upstream", n)
	}
}

// A run left "running" with a cancelled node (state written before the
// revive fix, or by another path) settles instead of spinning forever.
func TestSchedulerSettlesRunWithCancelledNode(t *testing.T) {
	t.Parallel()
	wf := domain.WorkflowDefinition{ID: "parallel-cancelled-settle", Name: "parallel cancelled settle", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{scriptNode("left", "true"), scriptNode("right", "true")},
	}
	e, s := parallelTestEngine(t, wf)
	now := time.Now().UTC()
	run := domain.Run{ID: "run-cancelled-settle", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{
			"left":  {NodeID: "left", Status: "pending"},
			"right": {NodeID: "right", Status: "cancelled", Attempt: 1},
		}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.acquireLease(run); err != nil {
		t.Fatal(err)
	}
	e.resume(run.ID)
	waitFor(t, s, run.ID, "failed")
	got, _ := s.Run(run.ID)
	if got.Nodes["left"].Status != "succeeded" || got.Nodes["right"].Status != "cancelled" {
		t.Fatalf("left=%s right=%s", got.Nodes["left"].Status, got.Nodes["right"].Status)
	}
}

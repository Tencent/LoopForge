package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func newRecoveryEngine(t *testing.T) (*Engine, *repo.Store) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := e.Drain(ctx); err != nil {
			t.Errorf("drain recovery engine: %v", err)
		}
	})
	return e, s
}

func TestResumeInterruptedRunsResumesOrphansAndSparesGatedRuns(t *testing.T) {
	e, s := newRecoveryEngine(t)
	now := time.Now().UTC()
	orphan := domain.Run{ID: "run-orphan", WorkflowID: "wf", Status: "running", CreatedAt: now, UpdatedAt: now,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Nodes: []domain.WorkflowNode{
			{ID: "a", Type: "command", Command: "true"}, {ID: "b", Type: "command", Command: "true"},
		}, Edges: []domain.WorkflowEdge{{Source: "a", Target: "b", When: "success"}}},
		Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "running"}, "b": {NodeID: "b", Status: "pending"}}}
	gated := domain.Run{ID: "run-gated", WorkflowID: "wf", Status: "waiting", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "waiting_approval"}}}
	done := domain.Run{ID: "run-done", WorkflowID: "wf", Status: "succeeded", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "succeeded"}}}
	for _, r := range []domain.Run{orphan, gated, done} {
		if err := s.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}

	// Inspect recovery before the resumed scheduler starts the next node.
	// An empty workflow could finish between ResumeInterruptedRuns and Run.
	unlock := e.lockRunExec(orphan.ID)
	defer unlock()
	resumed := e.ResumeInterruptedRuns()
	if len(resumed) != 1 || resumed[0] != "run-orphan" {
		t.Fatalf("expected only run-orphan to be resumed, got %v", resumed)
	}

	got, _ := s.Run("run-orphan")
	if got.Status != "running" {
		t.Fatalf("orphan run status = %q, want running", got.Status)
	}
	if got.Nodes["a"].Status != "pending" {
		t.Fatalf("in-flight node should reset to pending, got %+v", got.Nodes["a"])
	}
	if got.Nodes["b"].Status != "pending" {
		t.Fatalf("pending node should stay pending, got %q", got.Nodes["b"].Status)
	}
	if e.ActiveRunCount() != 1 {
		t.Fatalf("expected resumed scheduler, active=%d", e.ActiveRunCount())
	}

	if still, _ := s.Run("run-gated"); still.Status != "waiting" {
		t.Fatalf("gated run status = %q, want waiting", still.Status)
	}
	if still, _ := s.Run("run-done"); still.Status != "succeeded" {
		t.Fatalf("finished run status = %q, want succeeded", still.Status)
	}
}

func TestResumeInterruptedRunsSettlesCompletedGraph(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("worker", "host", time.Second, true)
	wf := domain.WorkflowDefinition{ID: "wf-stuck", Name: "stuck", Nodes: []domain.WorkflowNode{
		{ID: "analyze", Name: "analyze"}, {ID: "close", Name: "close"},
	}}
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-stuck-complete", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running",
		OwnerInstanceID: "dead-worker", LeaseUntil: now.Add(-time.Minute), Generation: 3,
		Nodes: map[string]*domain.NodeRun{
			"analyze": {NodeID: "analyze", Status: "succeeded"},
			"close":   {NodeID: "close", Status: "succeeded"},
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 0 {
		t.Fatalf("completed graph should settle, not resume: %v", resumed)
	}
	got, _ := s.Run(run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded", got.Status)
	}
	found := false
	for _, ev := range got.Events {
		if ev.Type == "run.succeeded" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing run.succeeded event: %+v", got.Events)
	}
}

func TestResumeInterruptedRunsLeavesScheduledIntegrateToTheOwner(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("worker", "host", time.Minute, true)
	wf := domain.WorkflowDefinition{ID: "wf-align", Name: "align", Nodes: []domain.WorkflowNode{
		{ID: "resolve", Name: "resolve"}, {ID: "verify", Name: "verify"},
	}}
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-align-open", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running",
		OwnerInstanceID: "worker", LeaseUntil: now.Add(time.Minute), Generation: 2,
		ExecutionContext: domain.TelemetryContext{ScheduleJobID: "job-align"},
		Repository: domain.RepositoryContext{
			RepositoryID: "skillhub", WorkBranch: "agentflow/align-default", IntegrateBranch: "agentflow/integration",
		},
		Nodes: map[string]*domain.NodeRun{
			"resolve": {NodeID: "resolve", Status: "succeeded"},
			"verify":  {NodeID: "verify", Status: "succeeded"},
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 0 {
		t.Fatalf("in-flight integrate should stay with its owner, resumed %v", resumed)
	}
	got, _ := s.Run(run.ID)
	if got.Status != "running" {
		t.Fatalf("status = %q, want running until integrate finishes", got.Status)
	}
	for _, ev := range got.Events {
		if ev.Type == "run.succeeded" {
			t.Fatal("recovery marked the run succeeded before integrate")
		}
	}
}

func TestResumeInterruptedRunsResumesScheduledRunSoIntegrateCanFinish(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("worker", "host", time.Minute, true)
	wf := domain.WorkflowDefinition{ID: "wf-align-orphan", Name: "align", Nodes: []domain.WorkflowNode{
		{ID: "resolve", Name: "resolve"}, {ID: "verify", Name: "verify"},
	}}
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-align-orphan", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running",
		OwnerInstanceID: "dead-worker", LeaseUntil: now.Add(-time.Minute), Generation: 1,
		ExecutionContext: domain.TelemetryContext{ScheduleJobID: "job-align"},
		Repository: domain.RepositoryContext{
			RepositoryID: "skillhub", WorkBranch: "agentflow/align-default", IntegrateBranch: "agentflow/integration",
		},
		Nodes: map[string]*domain.NodeRun{
			"resolve": {NodeID: "resolve", Status: "succeeded"},
			"verify":  {NodeID: "verify", Status: "succeeded"},
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	resumed := e.ResumeInterruptedRuns()
	if len(resumed) != 1 || resumed[0] != run.ID {
		t.Fatalf("orphan scheduled run should resume for integrate, got %v", resumed)
	}
	deadline := time.Now().Add(3 * time.Second)
	var got domain.Run
	for time.Now().Before(deadline) {
		got, _ = s.Run(run.ID)
		if got.Status == "failed" || got.Status == "succeeded" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed from the integrate attempt", got.Status)
	}
	found := false
	for _, ev := range got.Events {
		if ev.Type == "run.failed" && strings.Contains(ev.Message, "合入 integration") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing integrate failure event: %+v", got.Events)
	}
}

func TestFinishAtKeepsRewoundRunAlive(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("worker", "host", time.Minute, true)
	wf := domain.WorkflowDefinition{
		ID: "wf-rework", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "fix", Name: "fix"},
			{ID: "verify", Name: "verify", OnFailure: "reject", RejectTo: "fix"},
		},
		Edges: []domain.WorkflowEdge{{Source: "fix", Target: "verify"}},
	}
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-rewound", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running",
		Nodes: map[string]*domain.NodeRun{
			"fix":    {NodeID: "fix", Status: "pending", ReworkCount: 1},
			"verify": {NodeID: "verify", Status: "pending"},
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(run)
	if err != nil {
		t.Fatal(err)
	}
	e.finishAt(run.ID, owned.Generation, "failed", pipelineFailedMessage)
	got, _ := s.Run(run.ID)
	if got.Status != "running" {
		t.Fatalf("status = %q, want running after the failure was rewound", got.Status)
	}
	for _, ev := range got.Events {
		if ev.Type == "run.failed" {
			t.Fatal("rewound run was marked failed")
		}
	}
}

func TestFinishAtFailsWhenANodeIsStillFailed(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("worker", "host", time.Minute, true)
	wf := domain.WorkflowDefinition{
		ID: "wf-still-failed", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "fix", Name: "fix"},
			{ID: "verify", Name: "verify"},
			{ID: "close", Name: "close"},
		},
		Edges: []domain.WorkflowEdge{{Source: "fix", Target: "verify"}, {Source: "verify", Target: "close"}},
	}
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-still-failed", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running",
		Nodes: map[string]*domain.NodeRun{
			"fix":    {NodeID: "fix", Status: "succeeded"},
			"verify": {NodeID: "verify", Status: "failed"},
			"close":  {NodeID: "close", Status: "pending"},
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(run)
	if err != nil {
		t.Fatal(err)
	}
	e.finishAt(run.ID, owned.Generation, "failed", pipelineFailedMessage)
	got, _ := s.Run(run.ID)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
}

func TestFinishInterruptedSettlesCompletedGraphInsteadOfParking(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("owner", "host", time.Second, true)
	wf := domain.WorkflowDefinition{ID: "wf-drain", Nodes: []domain.WorkflowNode{{ID: "close", Name: "close"}}}
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-drain-complete", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running",
		Nodes:     map[string]*domain.NodeRun{"close": {NodeID: "close", Status: "succeeded"}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(run)
	if err != nil {
		t.Fatal(err)
	}
	e.finishInterruptedAt(run.ID, owned.Generation)
	got, _ := s.Run(run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("drain of a completed graph parked as %q, want succeeded", got.Status)
	}
}

func TestCancelSettlesOrphanedRunWithoutScheduler(t *testing.T) {
	e, s := newRecoveryEngine(t)
	now := time.Now().UTC()
	if err := s.PutRun(domain.Run{ID: "run-stale", WorkflowID: "wf", Status: "running", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "running"}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel("run-stale"); err != nil {
		t.Fatalf("cancel orphaned run: %v", err)
	}
	if got, _ := s.Run("run-stale"); got.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	if err := e.Cancel("run-stale"); err == nil {
		t.Fatal("expected error when cancelling a settled run")
	}
	if err := e.Cancel("run-missing"); err == nil {
		t.Fatal("expected error when cancelling a missing run")
	}
}

func TestResumeInterruptedRunsSkipsUnexpiredLease(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("instance-b", "host-b", time.Second, true)
	now := time.Now().UTC()
	r := domain.Run{ID: "run-live-owner", WorkflowID: "wf", Status: "running", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{"a": {NodeID: "a", Status: "running"}}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := s.TryAcquireRunLease(r.ID, "instance-a", 0, time.Second); err != nil {
		t.Fatal(err)
	}
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 0 {
		t.Fatalf("resumed live owner: %v", resumed)
	}
	got, _ := s.RunState(r.ID)
	if got.OwnerInstanceID != "instance-a" || got.Nodes["a"].Status != "running" {
		t.Fatalf("live lease changed: owner=%s node=%s", got.OwnerInstanceID, got.Nodes["a"].Status)
	}
}

func TestResumeInterruptedRunsTakesExpiredLeaseAndBumpsGeneration(t *testing.T) {
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("instance-b", "host-b", time.Second, true)
	now := time.Now().UTC()
	r := domain.Run{ID: "run-expired-owner", WorkflowID: "wf", Status: "running", CreatedAt: now, UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{
			"running": {NodeID: "running", Status: "running", Attempt: 4, StartedAt: now, SessionID: "sess-running"},
			"input":   {NodeID: "input", Status: "pending_input"},
			"gate":    {NodeID: "gate", Status: "waiting_approval"},
		}}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := s.TryAcquireRunLease(r.ID, "instance-a", 0, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	resumed := e.ResumeInterruptedRuns()
	if len(resumed) != 1 || resumed[0] != r.ID {
		t.Fatalf("resumed = %v", resumed)
	}
	got, _ := s.RunState(r.ID)
	if got.Generation != 2 || (got.OwnerInstanceID != "" && got.OwnerInstanceID != "instance-b") {
		t.Fatalf("owner=%s generation=%d", got.OwnerInstanceID, got.Generation)
	}
	if got.Nodes["running"].Status == "running" || got.Nodes["input"].Status == "pending_input" {
		t.Fatalf("in-flight nodes were not reset: %+v", got.Nodes)
	}
	if got.Nodes["gate"].Status != "waiting_approval" {
		t.Fatalf("approval gate changed: %s", got.Nodes["gate"].Status)
	}
	if got.Nodes["running"].Attempt != 4 {
		t.Fatalf("resume consumed an attempt: %d", got.Nodes["running"].Attempt)
	}
	if got.Nodes["running"].ResumeSessionID != "sess-running" {
		t.Fatalf("resume session=%q", got.Nodes["running"].ResumeSessionID)
	}
}

package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func leaseEngine(t *testing.T, s *repo.Store, id string) *Engine {
	t.Helper()
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	e.ConfigureExecution(id, id+"-host", 100*time.Millisecond, true)
	return e
}

func leaseRun(t *testing.T, s *repo.Store, id string, status string, nodeStatus string) domain.Run {
	t.Helper()
	now := time.Now().UTC()
	r := domain.Run{ID: id, Status: status, Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: nodeStatus}}, CreatedAt: now, UpdatedAt: now}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	return r
}

// retiredBindRun inserts a running run already bound to a host that no longer
// exists. The bind columns only persist on insert, so they cannot be added by
// mutating a run that leaseRun already wrote.
func retiredBindRun(t *testing.T, s *repo.Store, e *Engine, id string) domain.Run {
	t.Helper()
	now := time.Now().UTC()
	r := domain.Run{
		ID: id, Status: "running", WorkspaceHost: "retired-host", WorkspaceRoot: "/gone/workspaces",
		Nodes:     map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "running"}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.RunState(id)
	if !ok {
		t.Fatal("run missing after insert")
	}
	if !e.foreignWorkspaceBind(stored) {
		t.Fatalf("run must look foreign: host=%q root=%q", stored.WorkspaceHost, stored.WorkspaceRoot)
	}
	return stored
}

// heartbeatHost advertises host as served by a live worker.
func heartbeatHost(t *testing.T, s *repo.Store, instanceID, host string) {
	t.Helper()
	if err := s.HeartbeatEngineInstance(repo.EngineInstance{InstanceID: instanceID, Role: "worker", WorkspaceHost: host}, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestNonOwnerExecuteNodeDoesNotMutateRun(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	a, b := leaseEngine(t, s, "a"), leaseEngine(t, s, "b")
	r := leaseRun(t, s, "run-owner", "running", "pending")
	if _, err := a.acquireLease(r); err != nil {
		t.Fatal(err)
	}
	b.executeNode(context.Background(), r.ID, domain.WorkflowDefinition{}, domain.WorkflowNode{ID: "node", Name: "node", Type: "approval"})
	got, _ := s.RunState(r.ID)
	if got.Nodes["node"].Status != "pending" || got.Status != "running" {
		t.Fatalf("non-owner mutated run: status=%s node=%s", got.Status, got.Nodes["node"].Status)
	}
}

func TestStaleOwnerCannotFailAfterGenerationTakeover(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	a := leaseEngine(t, s, "a")
	b := leaseEngine(t, s, "b")
	a.leaseTTL = 15 * time.Millisecond
	r := leaseRun(t, s, "run-takeover", "running", "running")
	owned, err := a.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := b.acquireLease(owned); err != nil {
		t.Fatal(err)
	}
	a.nodeFailed(r.ID, domain.WorkflowNode{ID: "node", MaxAttempts: 1}, context.Canceled, false)
	a.finish(r.ID, "failed", "stale")
	got, _ := s.RunState(r.ID)
	if got.OwnerInstanceID != "b" || got.Generation != 2 || got.Nodes["node"].Status != "running" || got.Status != "running" {
		t.Fatalf("stale owner mutated takeover: owner=%s generation=%d status=%s node=%s", got.OwnerInstanceID, got.Generation, got.Status, got.Nodes["node"].Status)
	}
}

func TestFinishFailedIgnoredWhileNodeActive(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	r := leaseRun(t, s, "run-finish", "running", "running")
	if _, err := e.acquireLease(r); err != nil {
		t.Fatal(err)
	}
	e.finish(r.ID, "failed", "too early")
	got, _ := s.RunState(r.ID)
	if got.Status != "running" {
		t.Fatalf("status = %q, want running", got.Status)
	}
}

func TestExecuteNodeRestoresRunStatusToRunning(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	r := leaseRun(t, s, "run-restore", "failed", "pending")
	if _, err := e.acquireLease(r); err != nil {
		t.Fatal(err)
	}
	e.executeNode(context.Background(), r.ID, domain.WorkflowDefinition{}, domain.WorkflowNode{ID: "node", Name: "approval", Type: "approval"})
	got, _ := s.RunState(r.ID)
	if got.Status != "running" {
		t.Fatalf("status = %q, want running", got.Status)
	}
}

func TestStaleAttemptFailureIsIgnored(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	r := leaseRun(t, s, "run-attempt", "running", "running")
	r.Nodes["node"].Attempt = 2
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	e.nodeFailedAt(r.ID, domain.WorkflowNode{ID: "node", MaxAttempts: 2}, 1, owned.Generation, context.Canceled, false)
	got, _ := s.RunState(r.ID)
	if got.Nodes["node"].Status != "running" || got.Nodes["node"].Error != "" {
		t.Fatalf("stale attempt mutated node: %+v", got.Nodes["node"])
	}
}

func TestControlPlaneEnqueuesWithoutExecute(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "enqueue-only", Name: "enqueue", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "approval", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	api := leaseEngine(t, s, "api")
	api.ConfigureExecution("api", "api-host", time.Second, false)
	run, err := api.Start(wf.ID, "from studio")
	if err != nil {
		t.Fatal(err)
	}
	if run.OwnerInstanceID != "" || run.Status != "running" || api.ActiveRunCount() != 0 {
		t.Fatalf("control plane executed the run: owner=%q status=%s active=%d", run.OwnerInstanceID, run.Status, api.ActiveRunCount())
	}
	worker := leaseEngine(t, s, "worker")
	worker.ConfigureExecution("worker", "worker-host", time.Second, true)
	claimed := worker.ResumeInterruptedRuns()
	if len(claimed) != 1 || claimed[0] != run.ID {
		t.Fatalf("worker claimed %v", claimed)
	}
	if worker.ActiveRunCount() != 1 {
		t.Fatalf("worker active=%d", worker.ActiveRunCount())
	}
	t.Cleanup(func() { _ = worker.Cancel(run.ID) })
}

func TestControlPlaneCancelRequestsRunningWorker(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "cancel-handoff", Name: "cancel", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "command", Command: "sleep 30", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	api := leaseEngine(t, s, "api")
	api.ConfigureExecution("api", "host", time.Second, false)
	run, err := api.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	worker := leaseEngine(t, s, "worker")
	worker.ConfigureExecution("worker", "host", time.Second, true)
	if claimed := worker.ResumeInterruptedRuns(); len(claimed) != 1 {
		t.Fatalf("claimed %v", claimed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.RunState(run.ID)
		if got.OwnerInstanceID == "worker" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := api.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(run.ID)
	if !got.CancelRequested {
		t.Fatalf("cancel was not handed to the worker: %+v", got)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = s.RunState(run.ID)
		if got.Status == "cancelled" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("worker did not honour cancel request: status=%s", got.Status)
}

func TestControlPlaneCancelNotifiesWorkerOverHTTP(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "cancel-fast", Name: "cancel", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "command", Command: "sleep 30", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	api := leaseEngine(t, s, "api")
	api.ConfigureExecution("api", "host", 30*time.Second, false)
	run, err := api.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	worker := leaseEngine(t, s, "worker")
	worker.ConfigureExecution("worker", "host", 30*time.Second, true)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/runs/{id}/interrupt", worker.HandleRunInterrupt)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	api.SetRunControlNotify(HTTPRunControlNotify(srv.URL, ""))
	if claimed := worker.ResumeInterruptedRuns(); len(claimed) != 1 {
		t.Fatalf("claimed %v", claimed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.RunState(run.ID)
		if got.OwnerInstanceID == "worker" && worker.ActiveRunCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	started := time.Now()
	if err := api.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.RunState(run.ID)
		if got.Status == "cancelled" {
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("cancel took %s, want well under the 10s lease heartbeat", elapsed)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := s.RunState(run.ID)
	t.Fatalf("worker did not honour HTTP interrupt: status=%s after %s", got.Status, time.Since(started))
}

func TestInterruptRequiresDurableControlFlag(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "interrupt-guard", Name: "guard", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "command", Command: "sleep 30", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	worker := leaseEngine(t, s, "worker")
	run, err := worker.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if worker.ActiveRunCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if worker.ActiveRunCount() == 0 {
		t.Fatal("worker never started the run")
	}
	t.Cleanup(func() { _ = worker.Cancel(run.ID) })
	if worker.Interrupt(run.ID) {
		t.Fatal("interrupt without pause/cancel flag should be a no-op")
	}
	got, _ := s.RunState(run.ID)
	if got.Status != "running" {
		t.Fatalf("status=%s, want still running", got.Status)
	}
}

func interruptServer(t *testing.T, worker *Engine, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/runs/{id}/interrupt", func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		worker.HandleRunInterrupt(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestControlPlaneCancelRoutesToOwningWorker(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "cancel-owner", Name: "cancel", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "command", Command: "sleep 30", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	api := leaseEngine(t, s, "api")
	api.ConfigureExecution("api", "host", 30*time.Second, false)
	workerA := leaseEngine(t, s, "worker-a")
	workerA.ConfigureExecution("worker-a", "host-a", 30*time.Second, true)
	workerB := leaseEngine(t, s, "worker-b")
	workerB.ConfigureExecution("worker-b", "host-b", 30*time.Second, true)

	var hitsA, hitsB atomic.Int32
	srvA := interruptServer(t, workerA, &hitsA)
	srvB := interruptServer(t, workerB, &hitsB)
	if err := s.HeartbeatEngineInstance(repo.EngineInstance{InstanceID: "worker-a", Role: "worker", ControlURL: srvA.URL, WorkspaceHost: "host-a"}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatEngineInstance(repo.EngineInstance{InstanceID: "worker-b", Role: "worker", ControlURL: srvB.URL, WorkspaceHost: "host-b"}, 30*time.Second); err != nil {
		t.Fatal(err)
	}

	run, err := api.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if claimed := workerA.ResumeInterruptedRuns(); len(claimed) != 1 {
		t.Fatalf("worker-a claimed %v", claimed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.RunState(run.ID)
		if got.OwnerInstanceID == "worker-a" && workerA.ActiveRunCount() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	started := time.Now()
	if err := api.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.RunState(run.ID)
		if got.Status == "cancelled" {
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("cancel took %s", elapsed)
			}
			if hitsA.Load() != 1 {
				t.Fatalf("owner interrupt hits=%d, want 1", hitsA.Load())
			}
			if hitsB.Load() != 0 {
				t.Fatalf("other worker interrupt hits=%d, want 0", hitsB.Load())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := s.RunState(run.ID)
	t.Fatalf("owner worker did not honour routed interrupt: status=%s after %s hitsA=%d hitsB=%d", got.Status, time.Since(started), hitsA.Load(), hitsB.Load())
}

func TestInterruptRejectsBadControlToken(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	worker := leaseEngine(t, s, "worker")
	worker.ConfigureControl(ControlConfig{Token: "secret"})
	srv := interruptServer(t, worker, nil)
	do := func(token string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/internal/runs/missing/interrupt", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("X-Agentflow-Control-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if status := do(""); status != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d, want 401", status)
	}
	if status := do("secret"); status != http.StatusOK {
		t.Fatalf("valid token status=%d, want 200", status)
	}
}

func waitRun(t *testing.T, s *repo.Store, runID string, timeout time.Duration, ok func(domain.Run) bool) domain.Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var got domain.Run
	for time.Now().Before(deadline) {
		got, _ = s.RunState(runID)
		if ok(got) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach expected state: status=%s owner=%s host=%s root=%s", runID, got.Status, got.OwnerInstanceID, got.WorkspaceHost, got.WorkspaceRoot)
	return got
}

func TestAPIResumePausedIsClaimedOnlyByBoundWorker(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "pause-sticky", Name: "pause", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "command", Command: "sleep 30", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	api := leaseEngine(t, s, "api")
	api.ConfigureExecution("api", "api-host", 30*time.Second, false)
	workerA := leaseEngine(t, s, "worker-a")
	workerA.ConfigureExecution("worker-a", "worker-a-host", 30*time.Second, true)
	workerB := leaseEngine(t, s, "worker-b")
	workerB.ConfigureExecution("worker-b", "worker-b-host", 30*time.Second, true)
	heartbeatHost(t, s, "worker-a", "worker-a-host")
	api.SetRunControlNotify(func(runID string) { workerA.Interrupt(runID) })

	run, err := api.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if claimed := workerA.ResumeInterruptedRuns(); len(claimed) != 1 {
		t.Fatalf("worker-a claimed %v", claimed)
	}
	bound := waitRun(t, s, run.ID, 2*time.Second, func(got domain.Run) bool {
		return got.OwnerInstanceID == "worker-a" && got.WorkspaceHost == "worker-a-host" && got.WorkspaceRoot != "" && workerA.ActiveRunCount() > 0
	})
	if err := api.Pause(run.ID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, run.ID, 2*time.Second, func(got domain.Run) bool {
		return got.Status == "waiting" && got.PauseRequested
	})
	if err := api.ResumePaused(run.ID, ""); err != nil {
		t.Fatal(err)
	}
	resumed := waitRun(t, s, run.ID, 2*time.Second, func(got domain.Run) bool {
		return got.Status == "running" && !got.PauseRequested
	})
	if resumed.WorkspaceHost != bound.WorkspaceHost || resumed.WorkspaceRoot != bound.WorkspaceRoot {
		t.Fatalf("resume dropped workspace bind: host=%s root=%s", resumed.WorkspaceHost, resumed.WorkspaceRoot)
	}
	if claimed := workerB.ResumeInterruptedRuns(); len(claimed) != 0 {
		t.Fatalf("worker-b stole paused resume: %v", claimed)
	}
	if claimed := workerA.ResumeInterruptedRuns(); len(claimed) != 1 || claimed[0] != run.ID {
		t.Fatalf("worker-a did not reclaim paused resume: %v", claimed)
	}
	t.Cleanup(func() { _ = api.Cancel(run.ID); workerA.Interrupt(run.ID) })
	waitRun(t, s, run.ID, 2*time.Second, func(got domain.Run) bool {
		return got.OwnerInstanceID == "worker-a"
	})
}

func TestAPIRetryIsClaimedOnlyByBoundWorker(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "retry-sticky", Name: "retry", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "fail", Name: "fail", Type: "command", Command: "false", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	api := leaseEngine(t, s, "api")
	api.ConfigureExecution("api", "api-host", 30*time.Second, false)
	workerA := leaseEngine(t, s, "worker-a")
	workerA.ConfigureExecution("worker-a", "worker-a-host", 30*time.Second, true)
	workerB := leaseEngine(t, s, "worker-b")
	workerB.ConfigureExecution("worker-b", "worker-b-host", 30*time.Second, true)
	heartbeatHost(t, s, "worker-a", "worker-a-host")

	run, err := api.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if claimed := workerA.ResumeInterruptedRuns(); len(claimed) != 1 {
		t.Fatalf("worker-a claimed %v", claimed)
	}
	bound := waitRun(t, s, run.ID, 2*time.Second, func(got domain.Run) bool {
		return got.Status == "failed" && got.WorkspaceHost == "worker-a-host" && got.WorkspaceRoot != ""
	})
	if err := api.Retry(run.ID, "fail", "retry", ""); err != nil {
		t.Fatal(err)
	}
	retried := waitRun(t, s, run.ID, 2*time.Second, func(got domain.Run) bool {
		return got.Status == "running"
	})
	if retried.WorkspaceHost != bound.WorkspaceHost || retried.WorkspaceRoot != bound.WorkspaceRoot {
		t.Fatalf("retry dropped workspace bind: host=%s root=%s", retried.WorkspaceHost, retried.WorkspaceRoot)
	}
	if claimed := workerB.ResumeInterruptedRuns(); len(claimed) != 0 {
		t.Fatalf("worker-b stole retry: %v", claimed)
	}
	if claimed := workerA.ResumeInterruptedRuns(); len(claimed) != 1 || claimed[0] != run.ID {
		t.Fatalf("worker-a did not reclaim retry: %v", claimed)
	}
	t.Cleanup(func() { _ = api.Cancel(run.ID) })
	waitRun(t, s, run.ID, 2*time.Second, func(got domain.Run) bool {
		return got.OwnerInstanceID == "worker-a"
	})
}

func TestStartInsertsLeasedRunAndBlocksOtherResume(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "lease-start", Name: "lease start", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "command", Command: "sleep 30", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	a := leaseEngine(t, s, "instance-a")
	a.leaseTTL = time.Second
	b := leaseEngine(t, s, "instance-b")
	b.leaseTTL = time.Second
	run, err := a.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Cancel(run.ID) })
	if run.OwnerInstanceID != "instance-a" || run.Generation != 1 || run.LeaseUntil.IsZero() {
		t.Fatalf("start did not persist lease: owner=%s generation=%d lease=%v", run.OwnerInstanceID, run.Generation, run.LeaseUntil)
	}
	if resumed := b.ResumeInterruptedRuns(); len(resumed) != 0 {
		t.Fatalf("second instance resumed live start: %v", resumed)
	}
	got, _ := s.RunState(run.ID)
	if got.OwnerInstanceID != "instance-a" || got.Generation != 1 {
		t.Fatalf("lease stolen: owner=%s generation=%d", got.OwnerInstanceID, got.Generation)
	}
	if b.ActiveRunCount() != 0 {
		t.Fatalf("second instance started execute: %d", b.ActiveRunCount())
	}
}

// 回归：run 绑在已下线的 host 上时，worker 仍然要认取消请求。API 进程执行被
// 关闭，Engine.Cancel 拿不到租约只能静默返回，没人结算 run 就永远是 running。
func TestResumeSettlesCancelForRetiredWorkspaceBind(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "worker")
	r := retiredBindRun(t, s, e, "run-retired-cancel")
	if _, err := s.RequestRunCancel(r.ID); err != nil {
		t.Fatal(err)
	}
	e.ResumeInterruptedRuns()
	got, _ := s.RunState(r.ID)
	if got.Status != "cancelled" {
		t.Fatalf("retired-bind cancel left run %q, want cancelled", got.Status)
	}
	if got.Nodes["node"].Status != "cancelled" {
		t.Fatalf("node status = %q, want cancelled", got.Nodes["node"].Status)
	}
}

// 没有取消/暂停请求时，绑定 host 仍有存活 worker 的 run 不能被接管执行。
func TestResumeStillSkipsLiveForeignWorkspaceBindWithoutRequest(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "worker")
	r := retiredBindRun(t, s, e, "run-retired-idle")
	heartbeatHost(t, s, "retired-worker", "retired-host")
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 0 {
		t.Fatalf("foreign bind resumed without a control request: %v", resumed)
	}
	got, _ := s.RunState(r.ID)
	if got.Status != "running" || got.OwnerInstanceID != "" {
		t.Fatalf("foreign bind was claimed: status=%q owner=%q", got.Status, got.OwnerInstanceID)
	}
}

func TestSameInstanceResumeDoesNotStartSecondExecute(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "lease-same", Name: "lease same", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "command", Command: "sleep 30", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	a := leaseEngine(t, s, "instance-same")
	a.leaseTTL = time.Second
	b := leaseEngine(t, s, "instance-same")
	b.leaseTTL = time.Second
	run, err := a.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Cancel(run.ID) })
	if resumed := b.ResumeInterruptedRuns(); len(resumed) != 0 {
		t.Fatalf("same instance resumed live start: %v", resumed)
	}
	if b.ActiveRunCount() != 0 {
		t.Fatalf("same instance started second execute: %d", b.ActiveRunCount())
	}
	got, _ := s.RunState(run.ID)
	if got.OwnerInstanceID != "instance-same" || got.Generation != 1 {
		t.Fatalf("lease changed: owner=%s generation=%d", got.OwnerInstanceID, got.Generation)
	}
}

func TestShouldCommitNodeOutcome(t *testing.T) {
	r := domain.Run{Generation: 1}
	running := &domain.NodeRun{Status: "running", Attempt: 8}
	if !shouldCommitNodeOutcome(r, running, 1, 1, true) {
		t.Fatal("success should commit after attempt bump")
	}
	if shouldCommitNodeOutcome(r, running, 2, 1, true) {
		t.Fatal("success must not commit after generation takeover")
	}
	if !shouldCommitNodeOutcome(r, running, 1, 8, false) {
		t.Fatal("matching attempt may fail")
	}
	if shouldCommitNodeOutcome(r, running, 1, 1, false) {
		t.Fatal("stale attempt must not fail")
	}
	succeeded := &domain.NodeRun{Status: "succeeded", Attempt: 1}
	if shouldCommitNodeOutcome(r, succeeded, 1, 8, false) {
		t.Fatal("failed must not overwrite succeeded")
	}
	if shouldCommitNodeOutcome(r, succeeded, 1, 1, true) {
		t.Fatal("success must not rewrite succeeded")
	}
}

func TestNodeFailedDoesNotOverwriteSucceeded(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	r := leaseRun(t, s, "run-success", "running", "succeeded")
	r.Nodes["node"].Attempt = 1
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	e.nodeFailedAt(r.ID, domain.WorkflowNode{ID: "node", MaxAttempts: 10}, 1, owned.Generation, errors.New("exit status 126"), false)
	got, _ := s.RunState(r.ID)
	if got.Nodes["node"].Status != "succeeded" {
		t.Fatalf("126 retry overwrote success: %+v", got.Nodes["node"])
	}
}

func TestFinishInterruptedParksUnlessUserCancel(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	e.ConfigureExecution("owner", "owner-host", time.Second, true)

	r := leaseRun(t, s, "run-park", "running", "running")
	r.Nodes["node"].Attempt = 2
	r.Nodes["node"].StartedAt = time.Now().UTC()
	r.Nodes["node"].SessionID = "sess-1"
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	e.finishInterruptedAt(r.ID, owned.Generation)
	got, _ := s.RunState(r.ID)
	if got.Status != "running" || got.Nodes["node"].Status != "pending" {
		t.Fatalf("deploy interrupt should park: status=%s node=%s", got.Status, got.Nodes["node"].Status)
	}
	if got.Nodes["node"].Attempt != 2 {
		t.Fatalf("attempt=%d, want 2", got.Nodes["node"].Attempt)
	}
	if got.Nodes["node"].ResumeSessionID != "sess-1" {
		t.Fatalf("resume session=%q", got.Nodes["node"].ResumeSessionID)
	}

	cancelRun := leaseRun(t, s, "run-cancel", "running", "running")
	if _, err := s.RequestRunCancel(cancelRun.ID); err != nil {
		t.Fatal(err)
	}
	cancelRun, _ = s.RunState(cancelRun.ID)
	owned, err = e.acquireLease(cancelRun)
	if err != nil {
		t.Fatal(err)
	}
	e.finishInterruptedAt(cancelRun.ID, owned.Generation)
	got, _ = s.RunState(cancelRun.ID)
	if got.Status != "cancelled" {
		t.Fatalf("user cancel status=%s", got.Status)
	}
}

func TestSandboxInterruptRequeuesNodeWithoutFailing(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	e.ConfigureExecution("owner", "owner-host", time.Second, true)
	r := leaseRun(t, s, "run-exec-lost", "running", "running")
	r.Nodes["node"].Attempt = 4
	r.Nodes["node"].StartedAt = time.Now().UTC()
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	e.requeueInterruptedNode(r.ID, "node", r.Nodes["node"].Attempt, owned.Generation)
	got, _ := s.RunState(r.ID)
	if got.Status != "running" || got.Nodes["node"].Status != "pending" || got.Nodes["node"].Error != "" {
		t.Fatalf("interrupt should requeue: %+v", got.Nodes["node"])
	}
	if got.Nodes["node"].Attempt != 4 {
		t.Fatalf("attempt=%d, want 4", got.Nodes["node"].Attempt)
	}
}

func TestRenewRunLeaseStopsOnLost(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	e := leaseEngine(t, s, "owner")
	r := leaseRun(t, s, "run-renew", "running", "running")
	owned, err := e.acquireLease(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.renewRunLease(r.ID, owned.Generation); err != nil {
		t.Fatalf("owner renew: %v", err)
	}
	if err := e.renewRunLease(r.ID, owned.Generation+1); !errors.Is(err, repo.ErrRunLeaseLost) {
		t.Fatalf("stale renew = %v, want ErrRunLeaseLost", err)
	}
}

func TestDrainParksRunningRunForResume(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "drain-park", Name: "drain park", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "wait", Name: "wait", Type: "command", Command: "sleep 30", MaxAttempts: 3,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a := New(s, runtime.NewManagerWithLimit(root, 1))
	a.ConfigureExecution("worker-a", "shared-host", 2*time.Second, true)
	run, err := a.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.RunState(run.ID)
		if nr := got.Nodes["wait"]; nr != nil && nr.Status == "running" && nr.Attempt == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Drain(drainCtx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if a.ActiveRunCount() != 0 {
		t.Fatalf("active=%d", a.ActiveRunCount())
	}
	got, _ := s.RunState(run.ID)
	if got.Status != "running" {
		t.Fatalf("status=%s, want running", got.Status)
	}
	if got.Nodes["wait"].Status != "pending" {
		t.Fatalf("node=%s, want pending", got.Nodes["wait"].Status)
	}
	if got.Nodes["wait"].Attempt != 1 {
		t.Fatalf("attempt=%d, want 1", got.Nodes["wait"].Attempt)
	}
	b := New(s, runtime.NewManagerWithLimit(root, 1))
	b.ConfigureExecution("worker-b", "shared-host", 2*time.Second, true)
	claimed := b.ResumeInterruptedRuns()
	if len(claimed) != 1 {
		t.Fatalf("claimed %v status=%s owner=%s host=%s root=%s bind=%q", claimed, got.Status, got.OwnerInstanceID, got.WorkspaceHost, got.WorkspaceRoot, b.sandboxes.Root())
	}
	t.Cleanup(func() { _ = b.Cancel(run.ID) })
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = s.RunState(run.ID)
		if got.Nodes["wait"].Status == "running" {
			if got.Nodes["wait"].Attempt != 1 {
				t.Fatalf("resume bumped attempt to %d", got.Nodes["wait"].Attempt)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("worker-b did not resume node: %+v", got.Nodes["wait"])
}

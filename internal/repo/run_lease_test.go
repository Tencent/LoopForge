package repo

import (
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func putLeaseTestRun(t *testing.T, s *Store, id string) domain.Run {
	t.Helper()
	now := time.Now().UTC()
	r := domain.Run{ID: id, Status: "running", Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "pending"}}, CreatedAt: now, UpdatedAt: now}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunLeaseMutualExclusionAndExpiryTakeover(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := putLeaseTestRun(t, s, "run-lease")
	if err := s.TryAcquireRunLease(r.ID, "instance-a", r.Generation, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	owned, _ := s.RunState(r.ID)
	if owned.Generation != 1 {
		t.Fatalf("initial generation = %d, want 1", owned.Generation)
	}
	if err := s.TryAcquireRunLease(r.ID, "instance-b", owned.Generation, time.Second); !errors.Is(err, ErrRunLeaseHeld) {
		t.Fatalf("second acquire error = %v, want ErrRunLeaseHeld", err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := s.TryAcquireRunLease(r.ID, "instance-b", owned.Generation, time.Second); err != nil {
		t.Fatal(err)
	}
	taken, _ := s.RunState(r.ID)
	if taken.OwnerInstanceID != "instance-b" || taken.Generation != 2 {
		t.Fatalf("takeover = owner %q generation %d", taken.OwnerInstanceID, taken.Generation)
	}
	if err := s.RenewRunLease(r.ID, "instance-a", 1, time.Second); !errors.Is(err, ErrRunLeaseLost) {
		t.Fatalf("stale renew error = %v, want ErrRunLeaseLost", err)
	}
	owned.Status = "failed"
	if err := s.PutRun(owned); !errors.Is(err, ErrRunLeaseLost) {
		t.Fatalf("stale PutRun error = %v, want ErrRunLeaseLost", err)
	}
}

func TestPutRunAllowsTerminalStatusAfterLeaseExpiry(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := putLeaseTestRun(t, s, "run-expired-success")
	if err := s.TryAcquireRunLease(r.ID, "instance-a", r.Generation, 15*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	owned, _ := s.RunState(r.ID)
	time.Sleep(25 * time.Millisecond)
	owned.Status = "running"
	if err := s.PutRun(owned); !errors.Is(err, ErrRunLeaseLost) {
		t.Fatalf("non-terminal PutRun after expiry error = %v, want ErrRunLeaseLost", err)
	}
	owned.Status = "succeeded"
	if err := s.PutRun(owned); err != nil {
		t.Fatalf("terminal PutRun after lease expiry: %v", err)
	}
	got, _ := s.RunState(r.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded", got.Status)
	}
}

func TestPutRunInsertsLeaseOnCreate(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	r := domain.Run{
		ID: "run-create-lease", Status: "running", OwnerInstanceID: "instance-a",
		Generation: 1, LeaseUntil: now.Add(time.Second),
		Nodes:     map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "pending"}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	got, ok := s.RunState(r.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if got.OwnerInstanceID != "instance-a" || got.Generation != 1 || got.LeaseUntil.IsZero() {
		t.Fatalf("insert dropped lease: owner=%s generation=%d lease=%v", got.OwnerInstanceID, got.Generation, got.LeaseUntil)
	}
}

func TestPutRunPreservesLeaseFields(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := putLeaseTestRun(t, s, "run-put")
	if err := s.TryAcquireRunLease(r.ID, "instance-a", 0, time.Second); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.RunState(r.ID)
	stale.OwnerInstanceID, stale.LeaseUntil = "stale", time.Time{}
	stale.Status = "waiting"
	if err := s.PutRun(stale); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(r.ID)
	if got.OwnerInstanceID != "instance-a" || got.Generation != 1 || got.LeaseUntil.IsZero() {
		t.Fatalf("PutRun overwrote lease: %+v", got)
	}
}

func TestBindRunWorkspaceIsFirstWriterWins(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := putLeaseTestRun(t, s, "run-bind")
	if err := s.TryAcquireRunLease(r.ID, "instance-a", 0, time.Second); err != nil {
		t.Fatal(err)
	}
	owned, _ := s.RunState(r.ID)
	first, err := s.BindRunWorkspace(owned.ID, "instance-a", owned.Generation, "host-a", "/ws-a")
	if err != nil || !first {
		t.Fatalf("first bind = %v err=%v", first, err)
	}
	again, err := s.BindRunWorkspace(owned.ID, "instance-a", owned.Generation, "host-a", "/ws-a")
	if err != nil || again {
		t.Fatalf("idempotent bind = %v err=%v", again, err)
	}
	if _, err := s.BindRunWorkspace(owned.ID, "instance-a", owned.Generation, "host-b", "/ws-b"); !errors.Is(err, ErrRunWorkspaceBound) {
		t.Fatalf("foreign bind error = %v, want ErrRunWorkspaceBound", err)
	}
}

// 回归：API 侧 RequestRunPause/RequestRunCancel 设置的控制位必须能扛过
// worker 的 PutRun 写回窗口（master 的 writeRun 故意不更新这两列）。
func TestPutRunPreservesControlFlags(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := putLeaseTestRun(t, s, "run-ctl")
	if _, err := s.RequestRunPause(r.ID); err != nil {
		t.Fatal(err)
	}
	// worker 视角：读出的 run 不携带暂停位（另一进程刚设置的），写回后
	// 控制位必须仍然保留。
	worker, _ := s.RunState(r.ID)
	worker.PauseRequested = false
	worker.Status = "waiting"
	if err := s.PutRun(worker); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(r.ID)
	if !got.PauseRequested {
		t.Fatal("worker PutRun cleared the pause request")
	}
	if _, err := s.RequestRunCancel(r.ID); err != nil {
		t.Fatal(err)
	}
	worker, _ = s.RunState(r.ID)
	worker.CancelRequested = false
	worker.PauseRequested = false
	if err := s.PutRun(worker); err != nil {
		t.Fatal(err)
	}
	got, _ = s.RunState(r.ID)
	if !got.CancelRequested {
		t.Fatal("worker PutRun cleared the cancel request")
	}
	if err := s.ClearRunControl(r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.RunState(r.ID)
	if got.CancelRequested || got.PauseRequested {
		t.Fatalf("ClearRunControl left flags set: cancel=%v pause=%v", got.CancelRequested, got.PauseRequested)
	}
}

func TestRunControlRequestedReadsFlagsOnly(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := putLeaseTestRun(t, s, "run-ctl-watch")
	if s.RunControlRequested(r.ID) {
		t.Fatal("idle run reported a control request")
	}
	if _, err := s.RequestRunPause(r.ID); err != nil {
		t.Fatal(err)
	}
	if !s.RunControlRequested(r.ID) {
		t.Fatal("pause request was not visible to the cheap watcher")
	}
}

func TestClearRunControlReportsUnknownRun(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ClearRunControl("nope"); err == nil || err.Error() != "run not found" {
		t.Fatalf("ClearRunControl(nope) = %v, want run not found", err)
	}
}

func TestWaitingRunCanBeExplicitlyPaused(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := domain.Run{ID: "pause-waiting", Status: "waiting", Nodes: map[string]*domain.NodeRun{"gate": {NodeID: "gate", Status: "waiting_approval"}}}
	if err = s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	paused, err := s.RequestRunPause(r.ID)
	full, _ := s.RunState(r.ID)
	if err != nil || !paused.PauseRequested || paused.Status != "waiting" || full.Nodes["gate"] == nil || full.Nodes["gate"].Status != "waiting_approval" {
		t.Fatal("pause changed pending approval", paused.Status, err)
	}
}

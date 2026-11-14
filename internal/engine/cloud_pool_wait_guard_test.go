package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A runner that comes online while the node is already waiting must be picked
// up by a later poll; checking the pool only once would strand the node until
// the deadline even though a machine is available.
func TestWaitForPoolRunnerPicksRunnerThatComesOnlineDuringWait(t *testing.T) {
	t.Parallel()
	e, s := retryEngine(t)
	run := domain.Run{ID: "pool-late", Status: "running", Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "running"}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = s.PutRunner(domain.Runner{ID: "cloud-late", Name: "cloud-late", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation, Labels: []string{"pool"}})
	}()
	runner, err := e.waitForPoolRunner(ctx, run.ID, "build", "pool")
	if err != nil || runner.ID != "cloud-late" {
		t.Fatalf("waitForPoolRunner = %+v, %v; want the runner that came online", runner, err)
	}
	current, ok := s.RunState(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if len(current.Events) != 1 || current.Events[0].Type != "execution.waiting" {
		t.Fatalf("the initial empty poll must announce the wait exactly once: %+v", current.Events)
	}
}

// When poolBindWait elapses without an online runner the wait ends with the
// user-facing timeout error, not a context error and not a bogus runner.
func TestWaitForPoolRunnerTimesOutAfterPoolBindWait(t *testing.T) {
	// Not parallel: overrides the package-level poolBindWait.
	previousWait := poolBindWait
	poolBindWait = 50 * time.Millisecond
	defer func() { poolBindWait = previousWait }()
	e, s := retryEngine(t)
	run := domain.Run{ID: "pool-timeout", Status: "running", Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "running"}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runner, err := e.waitForPoolRunner(ctx, run.ID, "build", "pool")
	if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("expired poolBindWait must yield the pool timeout error, got %v", err)
	}
	if want := "云端执行池（pool）在 0 分钟内没有在线的执行机器，请确认执行机器已启动并在线后重试"; err.Error() != want {
		t.Fatalf("timeout error = %q, want %q", err.Error(), want)
	}
	if runner.ID != "" {
		t.Fatalf("timed-out wait returned runner %+v", runner)
	}
	if ctx.Err() != nil {
		t.Fatal("wait must stop at poolBindWait, not run until the caller's context expires")
	}
	current, ok := s.RunState(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if len(current.Events) != 1 || current.Events[0].Type != "execution.waiting" || !strings.Contains(current.Events[0].Message, "云端执行池（pool）暂无在线的执行机器") {
		t.Fatalf("timeout must follow exactly one waiting announcement: %+v", current.Events)
	}
}

// bindPoolRunner must surface the wait failure and leave the run unbound
// instead of continuing with an empty runner ID.
func TestBindPoolRunnerPropagatesPoolWaitFailureWithoutBinding(t *testing.T) {
	t.Parallel()
	e, s := retryEngine(t)
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool", Projects: []string{"*"}})
	run := domain.Run{
		ID: "pool-bind-cancel", Status: "running",
		ExecutionContext: domain.TelemetryContext{ProjectID: "p", ExecutionMode: domain.ExecutionModeRunner, ExecutionPool: domain.ExecutionModeCloud},
		Nodes:            map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "running"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got, err := e.bindPoolRunner(ctx, run, "build")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bindPoolRunner must return the wait error: %v", err)
	}
	if got.ExecutionContext.ExecutionRunnerID != "" {
		t.Fatalf("failed wait returned a bound run: %+v", got.ExecutionContext)
	}
	current, ok := s.RunState(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if current.ExecutionContext.ExecutionRunnerID != "" {
		t.Fatalf("failed wait must not persist a runner binding: %+v", current.ExecutionContext)
	}
	for _, ev := range current.Events {
		if ev.Type == "execution.bound" {
			t.Fatalf("failed wait must not announce a binding: %+v", current.Events)
		}
	}
}

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWaitForPoolRunnerReturnsOnlineRunnerWithoutWaiting(t *testing.T) {
	t.Parallel()
	e, s := retryEngine(t)
	run := domain.Run{ID: "pool-ready", Status: "running", Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "running"}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunner(domain.Runner{ID: "cloud-1", Name: "cloud-1", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation, Labels: []string{"pool"}}); err != nil {
		t.Fatal(err)
	}
	runner, err := e.waitForPoolRunner(context.Background(), run.ID, "build", "pool")
	if err != nil || runner.ID != "cloud-1" {
		t.Fatalf("waitForPoolRunner = %+v, %v; want cloud-1", runner, err)
	}
	current, ok := s.RunState(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if len(current.Events) != 0 {
		t.Fatalf("an immediately available runner must not announce a wait: %+v", current.Events)
	}
}

func TestWaitForPoolRunnerAnnouncesOnceAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	e, s := retryEngine(t)
	run := domain.Run{ID: "pool-empty", Status: "running", Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "running"}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	runner, err := e.waitForPoolRunner(ctx, run.ID, "build", "pool")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting must honor cancellation: %v", err)
	}
	if runner.ID != "" {
		t.Fatalf("canceled wait returned runner %+v", runner)
	}
	current, ok := s.RunState(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if len(current.Events) != 1 {
		t.Fatalf("wait must be announced exactly once across polls: %+v", current.Events)
	}
	if ev := current.Events[0]; ev.Type != "execution.waiting" || ev.NodeID != "build" || !strings.Contains(ev.Message, "云端执行池（pool）暂无在线的执行机器") {
		t.Fatalf("unexpected waiting event: %+v", ev)
	}
}

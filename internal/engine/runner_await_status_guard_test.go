package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	flowruntime "agentflow/internal/runtime"
)

// newAwaitGuardEngine returns an engine that owns a live run, so observations
// forwarded through e.observe are persisted only when awaitRunnerTask flushes.
func newAwaitGuardEngine(t *testing.T) (*Engine, *repo.Store, domain.Run) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := New(s, flowruntime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	e.ConfigureExecution("worker-guard", "worker-host", 30*time.Second, true)
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-await-guard", Status: "running", OwnerInstanceID: "worker-guard", Generation: 1,
		LeaseUntil: now.Add(time.Minute), Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running"}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return e, s, run
}

func putAwaitGuardTask(t *testing.T, s *repo.Store, id, status string) domain.RunnerTask {
	t.Helper()
	task := domain.RunnerTask{
		ID: id, RunnerID: "runner-guard", Provider: "codex", Status: status,
		LeaseOwner: "runner-guard", MaxAttempts: 1, RunID: "run-await-guard", NodeID: "work",
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	return task
}

func canceledAwaitContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func awaitGuardEventNames(events []domain.TelemetryEvent) string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	return strings.Join(names, ",")
}

func persistedTelemetryHas(t *testing.T, s *repo.Store, runID, name string) bool {
	t.Helper()
	run, ok := s.Run(runID)
	if !ok {
		t.Fatalf("run %s missing", runID)
	}
	for _, event := range run.Telemetry {
		if event.Name == name {
			return true
		}
	}
	return false
}

// With a canceled context the await loop observes the task exactly twice
// (before and after subscribing). Durable events must be bridged once and
// before the status event, the status must be reported once, and the live
// task must be flagged for cancellation.
func TestAwaitRunnerTaskObservesEventsBeforeStatusOnce(t *testing.T) {
	e, s, _ := newAwaitGuardEngine(t)
	task := putAwaitGuardTask(t, s, "rt-guard-once", domain.RunnerTaskLeased)
	if _, err := s.AppendRunnerTaskEvents(task.ID, []domain.RunnerTaskEvent{
		{Seq: 1, Type: "observation", Data: `{"name":"model.note","message":"note"}`, Ts: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	var events []domain.TelemetryEvent
	_, err := e.awaitRunnerTask(canceledAwaitContext(), task.ID, func(event domain.TelemetryEvent) {
		events = append(events, event)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("await err = %v, want context.Canceled", err)
	}
	if got := awaitGuardEventNames(events); got != "model.note,runner.claimed" {
		t.Fatalf("sink events = %q, want bridged event once then a single runner.claimed", got)
	}
	if claimed := events[1]; claimed.Provider != "codex" || claimed.Source.Provider != "codex" || claimed.Phase != "progress" {
		t.Fatalf("claimed event = %+v", claimed)
	}
	if got, ok := s.RunnerTask(task.ID); !ok || got.DesiredState != domain.RunnerTaskDesiredCancel {
		t.Fatalf("leased task after canceled await = %+v %v, want desired cancel", got, ok)
	}
}

// Callers such as alignment and integration await without a sink; status
// changes must not be reported (or panic) and cancellation still reaches the
// durable task.
func TestAwaitRunnerTaskWithoutSinkCancelsTask(t *testing.T) {
	e, s, _ := newAwaitGuardEngine(t)
	leased := putAwaitGuardTask(t, s, "rt-guard-nil-leased", domain.RunnerTaskLeased)
	queued := putAwaitGuardTask(t, s, "rt-guard-nil-queued", domain.RunnerTaskQueued)
	for _, id := range []string{leased.ID, queued.ID} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("await %s without sink panicked: %v", id, r)
				}
			}()
			if _, err := e.awaitRunnerTask(canceledAwaitContext(), id, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("await %s err = %v, want context.Canceled", id, err)
			}
		}()
	}
	if got, ok := s.RunnerTask(leased.ID); !ok || got.DesiredState != domain.RunnerTaskDesiredCancel {
		t.Fatalf("leased task = %+v %v, want desired cancel", got, ok)
	}
	if got, ok := s.RunnerTask(queued.ID); !ok || got.Status != domain.RunnerTaskCanceled {
		t.Fatalf("queued task = %+v %v, want canceled", got, ok)
	}
}

func TestAwaitRunnerTaskMissingTaskNamesTask(t *testing.T) {
	e, _, _ := newAwaitGuardEngine(t)
	called := false
	_, err := e.awaitRunnerTask(context.Background(), "rt-guard-gone", func(domain.TelemetryEvent) { called = true })
	if err == nil || !strings.Contains(err.Error(), "rt-guard-gone") || !strings.Contains(err.Error(), "disappeared") {
		t.Fatalf("missing task err = %v", err)
	}
	if called {
		t.Fatal("sink called for a missing task")
	}
}

// A completion landing between the first read and the subscription must be
// caught by the re-read, even when the context is already canceled, and the
// observations queued so far must be flushed into the run.
func TestAwaitRunnerTaskReReadAfterSubscribeCatchesCompletion(t *testing.T) {
	e, s, run := newAwaitGuardEngine(t)
	task := putAwaitGuardTask(t, s, "rt-guard-reread", domain.RunnerTaskLeased)
	var events []domain.TelemetryEvent
	got, err := e.awaitRunnerTask(canceledAwaitContext(), task.ID, func(event domain.TelemetryEvent) {
		events = append(events, event)
		next := task
		next.Status = domain.RunnerTaskSuccess
		if err := s.PutRunnerTask(next); err != nil {
			t.Error(err)
		}
		e.observe(run.ID, "work", event)
	})
	if err != nil || got.Status != domain.RunnerTaskSuccess {
		t.Fatalf("await = %+v %v, want the re-read to see success", got, err)
	}
	if names := awaitGuardEventNames(events); names != "runner.claimed" {
		t.Fatalf("sink events = %q", names)
	}
	if !persistedTelemetryHas(t, s, run.ID, "runner.claimed") {
		t.Fatal("observations from the first pass were not flushed")
	}
}

// A task that is already terminal on entry reports no status event and still
// flushes the observations it read.
func TestAwaitRunnerTaskAlreadyFinishedFlushesObservations(t *testing.T) {
	e, s, run := newAwaitGuardEngine(t)
	task := putAwaitGuardTask(t, s, "rt-guard-done", domain.RunnerTaskFailed)
	if _, err := s.AppendRunnerTaskEvents(task.ID, []domain.RunnerTaskEvent{
		{Seq: 1, Type: "observation", Data: `{"name":"model.final","message":"done"}`, Ts: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	var events []domain.TelemetryEvent
	got, err := e.awaitRunnerTask(canceledAwaitContext(), task.ID, func(event domain.TelemetryEvent) {
		events = append(events, event)
		e.observe(run.ID, "work", event)
	})
	if err != nil || got.Status != domain.RunnerTaskFailed {
		t.Fatalf("await = %+v %v, want failed task without error", got, err)
	}
	if names := awaitGuardEventNames(events); names != "model.final" {
		t.Fatalf("sink events = %q, terminal status must not add a status event", names)
	}
	if !persistedTelemetryHas(t, s, run.ID, "model.final") {
		t.Fatal("observations were not flushed for an already finished task")
	}
}

// Transitions driven from the sink are deterministic: the first read reports
// leased, the post-subscribe read reports running and wakes the waiter, and
// the next iteration returns the finished task.
func TestAwaitRunnerTaskReportsLeasedThenRunning(t *testing.T) {
	e, s, _ := newAwaitGuardEngine(t)
	task := putAwaitGuardTask(t, s, "rt-guard-walk", domain.RunnerTaskLeased)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var events []domain.TelemetryEvent
	got, err := e.awaitRunnerTask(ctx, task.ID, func(event domain.TelemetryEvent) {
		events = append(events, event)
		next := task
		switch event.Name {
		case "runner.claimed":
			next.Status = domain.RunnerTaskRunning
		case "runner.started":
			next.Status = domain.RunnerTaskSuccess
		default:
			return
		}
		if err := s.PutRunnerTask(next); err != nil {
			t.Error(err)
		}
		e.NotifyRunnerTaskChanged(task.ID)
	})
	if err != nil || got.Status != domain.RunnerTaskSuccess {
		t.Fatalf("await = %+v %v", got, err)
	}
	if names := awaitGuardEventNames(events); names != "runner.claimed,runner.started" {
		t.Fatalf("sink events = %q", names)
	}
	if started := events[1]; started.Category != "process" || started.Phase != "start" || started.Provider != "codex" {
		t.Fatalf("started event = %+v", started)
	}
}

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	flowruntime "agentflow/internal/runtime"
)

// These tests pin the awaitRunnerTask orchestration around the extracted
// observe step: every pass (before and after subscribing) forwards new runner
// events exactly once, reports each status transition once, and every exit
// path releases its task subscription.

func newAwaitGuardEngine(t *testing.T) (*repo.Store, *Engine) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, New(s, flowruntime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
}

func runnerTaskSubscriberCount(e *Engine, taskID string) (int, bool) {
	e.runnerTaskMu.Lock()
	defer e.runnerTaskMu.Unlock()
	subs, ok := e.runnerTaskSubs[taskID]
	return len(subs), ok
}

type awaitEventLog struct {
	mu     sync.Mutex
	events []domain.TelemetryEvent
	seen   chan string
}

func newAwaitEventLog() *awaitEventLog {
	return &awaitEventLog{seen: make(chan string, 64)}
}

func (l *awaitEventLog) sink(event domain.TelemetryEvent) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
	key := event.Name
	if !strings.HasPrefix(event.Name, "runner.") && event.Message != "" {
		key = "msg:" + event.Message
	}
	select {
	case l.seen <- key:
	default:
	}
}

func (l *awaitEventLog) waitFor(t *testing.T, key string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-l.seen:
			if got == key {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q; events so far: %+v", key, l.snapshot())
		}
	}
}

func (l *awaitEventLog) snapshot() []domain.TelemetryEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]domain.TelemetryEvent(nil), l.events...)
}

func TestAwaitRunnerTaskForwardsEachStatusAndEventOnce(t *testing.T) {
	t.Parallel()
	s, e := newAwaitGuardEngine(t)
	task := domain.RunnerTask{ID: "rt-await-once", RunnerID: "runner-1", Provider: "codex", Status: domain.RunnerTaskLeased, MaxAttempts: 1}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunnerTaskEvents(task.ID, []domain.RunnerTaskEvent{{Seq: 1, Type: "stdout", Data: "first", Ts: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}

	log := newAwaitEventLog()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		task domain.RunnerTask
		err  error
	}
	done := make(chan result, 1)
	go func() {
		got, err := e.awaitRunnerTask(ctx, task.ID, log.sink)
		done <- result{got, err}
	}()

	log.waitFor(t, "runner.claimed")
	// Wake the waiter without changing the status: the leased transition must
	// not be reported a second time.
	e.NotifyRunnerTaskChanged(task.ID)
	task.Status = domain.RunnerTaskRunning
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	e.NotifyRunnerTaskChanged(task.ID)
	log.waitFor(t, "runner.started")
	if _, err := s.AppendRunnerTaskEvents(task.ID, []domain.RunnerTaskEvent{{Seq: 2, Type: "stdout", Data: "second", Ts: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	e.NotifyRunnerTaskChanged(task.ID)
	log.waitFor(t, "msg:second")
	e.NotifyRunnerTaskChanged(task.ID)
	task.Status = domain.RunnerTaskSuccess
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	e.NotifyRunnerTaskChanged(task.ID)

	var res result
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitRunnerTask did not return after the task succeeded")
	}
	if res.err != nil || res.task.Status != domain.RunnerTaskSuccess {
		t.Fatalf("await task=%+v err=%v", res.task, res.err)
	}

	var statuses, messages []string
	for _, event := range log.snapshot() {
		if event.Name == "" {
			t.Fatalf("a status without a progress event (e.g. success) leaked an empty event: %+v", event)
		}
		if strings.HasPrefix(event.Name, "runner.") {
			statuses = append(statuses, event.Name)
			if event.Provider != "codex" || event.Source.Provider != "codex" || event.Source.Adapter != "runner-v1" {
				t.Fatalf("status event lost the task provider/source: %+v", event)
			}
			continue
		}
		if event.Message == "first" || event.Message == "second" {
			messages = append(messages, event.Message)
		}
	}
	if want := []string{"runner.claimed", "runner.started"}; !reflect.DeepEqual(statuses, want) {
		t.Fatalf("status events = %v, want %v (each transition reported once)", statuses, want)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(messages, want) {
		t.Fatalf("runner output events = %v, want %v (each durable event bridged once)", messages, want)
	}
	if n, ok := runnerTaskSubscriberCount(e, task.ID); ok || n != 0 {
		t.Fatalf("subscription leaked after completion: %d subscribers", n)
	}
}

// A task that finishes between the first read and the subscription must be
// picked up by the re-read right after subscribing, not by the 1s poll.
func TestAwaitRunnerTaskReReadAfterSubscribeReturnsImmediately(t *testing.T) {
	t.Parallel()
	s, e := newAwaitGuardEngine(t)
	task := domain.RunnerTask{ID: "rt-await-race", RunnerID: "runner-1", Provider: "codex", Status: domain.RunnerTaskLeased, MaxAttempts: 1}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	var claimed int
	sink := func(event domain.TelemetryEvent) {
		if event.Name != "runner.claimed" {
			return
		}
		claimed++
		// Runs inside the first observe pass, i.e. before subscribing. No
		// notification is sent, so only the post-subscribe re-read sees it.
		finished := task
		finished.Status = domain.RunnerTaskFailed
		if err := s.PutRunnerTask(finished); err != nil {
			t.Error(err)
		}
	}
	start := time.Now()
	got, err := e.awaitRunnerTask(context.Background(), task.ID, sink)
	elapsed := time.Since(start)
	if err != nil || got.Status != domain.RunnerTaskFailed {
		t.Fatalf("await task=%+v err=%v", got, err)
	}
	if elapsed >= 700*time.Millisecond {
		t.Fatalf("terminal status seen only by the fallback poll after %v; the post-subscribe re-read must catch it", elapsed)
	}
	if claimed != 1 {
		t.Fatalf("claimed reported %d times, want 1", claimed)
	}
	if n, ok := runnerTaskSubscriberCount(e, task.ID); ok || n != 0 {
		t.Fatalf("subscription leaked after the post-subscribe finish: %d subscribers", n)
	}
}

func TestAwaitRunnerTaskCancelReleasesSubscriptionAndFlagsTask(t *testing.T) {
	t.Parallel()
	s, e := newAwaitGuardEngine(t)
	task := domain.RunnerTask{ID: "rt-await-cancel", RunnerID: "runner-1", Provider: "codex", Status: domain.RunnerTaskRunning, MaxAttempts: 1}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	log := newAwaitEventLog()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.awaitRunnerTask(ctx, task.ID, log.sink)
		done <- err
	}()
	log.waitFor(t, "runner.started")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled await err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitRunnerTask ignored context cancellation")
	}
	stored, ok := s.RunnerTask(task.ID)
	if !ok || stored.DesiredState != domain.RunnerTaskDesiredCancel {
		t.Fatalf("running task was not flagged for cancel: %+v ok=%v", stored, ok)
	}
	if n, ok := runnerTaskSubscriberCount(e, task.ID); ok || n != 0 {
		t.Fatalf("subscription leaked after cancel: %d subscribers", n)
	}
}

// With no sink the waiter must still finish on every terminal status, and an
// in-flight (leased/running) task must not try to report progress to it.
func TestAwaitRunnerTaskWithoutSinkReturnsOnTerminalStatus(t *testing.T) {
	t.Parallel()
	s, e := newAwaitGuardEngine(t)
	inflight := domain.RunnerTask{ID: "rt-await-nil-inflight", RunnerID: "runner-1", Provider: "codex", Status: domain.RunnerTaskLeased, MaxAttempts: 1}
	if err := s.PutRunnerTask(inflight); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunnerTaskEvents(inflight.ID, []domain.RunnerTaskEvent{{Seq: 1, Type: "stdout", Data: "ignored", Ts: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- errors.New("awaitRunnerTask panicked with a nil sink")
			}
		}()
		got, err := e.awaitRunnerTask(ctx, inflight.ID, nil)
		if err == nil && got.Status != domain.RunnerTaskSuccess {
			err = errors.New("unexpected status " + got.Status)
		}
		done <- err
	}()
	for _, status := range []string{domain.RunnerTaskRunning, domain.RunnerTaskSuccess} {
		time.Sleep(20 * time.Millisecond)
		inflight.Status = status
		if err := s.PutRunnerTask(inflight); err != nil {
			t.Fatal(err)
		}
		e.NotifyRunnerTaskChanged(inflight.ID)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	for _, status := range []string{domain.RunnerTaskSuccess, domain.RunnerTaskFailed, domain.RunnerTaskCanceled} {
		task := domain.RunnerTask{ID: "rt-await-nil-" + status, RunnerID: "runner-1", Provider: "codex", Status: status, MaxAttempts: 1}
		if err := s.PutRunnerTask(task); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		got, err := e.awaitRunnerTask(ctx, task.ID, nil)
		cancel()
		if err != nil || got.Status != status {
			t.Fatalf("status %q: await task=%+v err=%v", status, got, err)
		}
	}
}

// A task that disappears between the first read and the post-subscribe
// re-read must surface as an error right away and release the subscription.
func TestAwaitRunnerTaskDisappearingAfterSubscribeFails(t *testing.T) {
	t.Parallel()
	s, e := newAwaitGuardEngine(t)
	task := domain.RunnerTask{ID: "rt-await-vanish", RunnerID: "runner-1", Provider: "codex", Status: domain.RunnerTaskLeased, MaxAttempts: 1}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	sink := func(event domain.TelemetryEvent) {
		if event.Name != "runner.claimed" {
			return
		}
		// Runs inside the first observe pass, before subscribing.
		if err := s.DB.Exec("DELETE FROM runner_tasks WHERE id = ?", task.ID).Error; err != nil {
			t.Error(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := e.awaitRunnerTask(ctx, task.ID, sink)
	if err == nil || !strings.Contains(err.Error(), "disappeared") {
		t.Fatalf("vanished task err=%v, want disappeared error", err)
	}
	if elapsed := time.Since(start); elapsed >= 700*time.Millisecond {
		t.Fatalf("disappearance noticed only after %v; the post-subscribe re-read must catch it", elapsed)
	}
	if n, ok := runnerTaskSubscriberCount(e, task.ID); ok || n != 0 {
		t.Fatalf("subscription leaked after the task disappeared: %d subscribers", n)
	}
}

// A task that is already terminal on the first read must still flush the
// observations bridged in that same pass into the owning run before returning.
func TestAwaitRunnerTaskFlushesObservationsWhenAlreadyFinished(t *testing.T) {
	t.Parallel()
	s, e := newAwaitGuardEngine(t)
	e.ConfigureExecution("worker-1", "worker-host", 30*time.Second, true)
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-await-flush", Status: "running", OwnerInstanceID: "worker-1", Generation: 1,
		LeaseUntil: now.Add(time.Minute), Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running"}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{
		ID: "rt-await-flush", RunnerID: "runner-1", Provider: "codex", Status: domain.RunnerTaskSuccess,
		MaxAttempts: 1, RunID: run.ID, NodeID: "work",
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	usage := domain.TokenUsage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}
	raw, err := json.Marshal(domain.TelemetryEvent{Category: "usage", Name: "usage.final", Phase: "finish", Usage: &usage})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunnerTaskEvents(task.ID, []domain.RunnerTaskEvent{{Seq: 1, Type: "observation", Data: string(raw), Ts: now}}); err != nil {
		t.Fatal(err)
	}
	// No observation flush loop runs here (StartMaintenance is not called),
	// so only awaitRunnerTask's own flush can persist the observation.
	got, err := e.awaitRunnerTask(context.Background(), task.ID, func(event domain.TelemetryEvent) {
		e.observe(run.ID, "work", event)
	})
	if err != nil || got.Status != domain.RunnerTaskSuccess {
		t.Fatalf("await task=%+v err=%v", got, err)
	}
	persisted, ok := s.Run(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if persisted.Usage.TotalTokens != 7 {
		t.Fatalf("observation bridged on the finishing pass was not flushed: usage=%+v telemetry=%+v", persisted.Usage, persisted.Telemetry)
	}
}

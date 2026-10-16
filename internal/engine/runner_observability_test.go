package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	flowruntime "agentflow/internal/runtime"
)

func TestAwaitRunnerTaskBridgesDurableEventsFromAnotherProcess(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := New(s, flowruntime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	e.ConfigureExecution("worker-1", "worker-host", 30*time.Second, true)
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-cross-process", Status: "running", OwnerInstanceID: "worker-1", Generation: 1,
		LeaseUntil: now.Add(time.Minute), Nodes: map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running"}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{
		ID: "rt-cross-process", RunnerID: "runner-1", Provider: "codebuddy",
		Status: domain.RunnerTaskRunning, LeaseOwner: "runner-1", MaxAttempts: 1, RunID: run.ID, NodeID: "work",
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observed := make(chan domain.TelemetryEvent, 4)
	done := make(chan error, 1)
	go func() {
		_, awaitErr := e.awaitRunnerTask(ctx, task.ID, func(event domain.TelemetryEvent) {
			observed <- event
			e.observe(run.ID, "work", event)
		})
		done <- awaitErr
	}()

	usage := domain.TokenUsage{InputTokens: 12, OutputTokens: 5, TotalTokens: 17}
	raw, err := json.Marshal(domain.TelemetryEvent{
		Category: "usage", Name: "usage.final", Phase: "finish", Usage: &usage,
		Source: domain.TelemetrySource{Layer: "executor", Adapter: "codebuddy-stream-json-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunnerTaskEvents(task.ID, []domain.RunnerTaskEvent{{Seq: 1, Type: "observation", Data: string(raw), Ts: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	task.Status = domain.RunnerTaskSuccess
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("worker did not observe cross-process runner completion")
	}
	var usageEvent *domain.TelemetryEvent
	for len(observed) > 0 {
		event := <-observed
		if event.Name == "usage.final" {
			copy := event
			usageEvent = &copy
		}
	}
	if usageEvent == nil || usageEvent.Usage == nil || usageEvent.Usage.TotalTokens != 17 || usageEvent.Source.Adapter != "codebuddy-stream-json-v1" {
		t.Fatalf("durable runner observation was not bridged correctly: %+v", usageEvent)
	}
	persisted, ok := s.Run(run.ID)
	if !ok {
		t.Fatal("run missing after runner event bridge")
	}
	found := false
	for _, event := range persisted.Telemetry {
		if event.Name == "usage.final" && event.Source.Adapter == "codebuddy-stream-json-v1" {
			found = true
		}
	}
	if !found || persisted.Usage.TotalTokens != 17 {
		t.Fatalf("runner observation was not persisted into the owner run: telemetry=%+v usage=%+v", persisted.Telemetry, persisted.Usage)
	}
}

func TestBridgeRunnerEventsSkipsAlreadyConsumedSequences(t *testing.T) {
	task := domain.RunnerTask{Provider: "codex"}
	events := []domain.RunnerTaskEvent{
		{Seq: 1, Type: "stdout", Data: "first"},
		{Seq: 2, Type: "stdout", Data: "second"},
	}
	var observed []domain.TelemetryEvent
	sink := func(event domain.TelemetryEvent) { observed = append(observed, event) }
	after := bridgeRunnerEventsToSink(task, events, 1, sink)
	if after != 2 || len(observed) != 1 || observed[0].Message != "second" {
		t.Fatalf("after=%d observations=%+v", after, observed)
	}
	bridgeRunnerEventsToSink(task, events, after, sink)
	if len(observed) != 1 {
		t.Fatalf("already-consumed events were bridged again: %+v", observed)
	}
}

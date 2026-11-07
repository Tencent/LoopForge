package domain

import (
	"testing"
	"time"
)

func timingEvents(t0 time.Time, specs ...any) []RunEvent {
	var out []RunEvent
	for i := 0; i+2 < len(specs); i += 3 {
		out = append(out, RunEvent{Timestamp: t0.Add(specs[i].(time.Duration)), NodeID: specs[i+1].(string), Type: specs[i+2].(string)})
	}
	return out
}

func TestRunTimingCountsEveryAttemptAndSkipsFailedWait(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	events := timingEvents(t0,
		time.Minute, "code", "node.started",
		11*time.Minute, "code", "node.failed",
		11*time.Minute, "", "run.failed",
		24*time.Hour, "code", "node.retry_requested",
		24*time.Hour, "code", "node.started",
		24*time.Hour+5*time.Minute, "code", "node.succeeded",
		24*time.Hour+5*time.Minute, "", "run.succeeded",
	)
	got := ComputeRunTiming(Run{Status: "succeeded", CreatedAt: t0}, events, t0.Add(72*time.Hour))
	if got.ExecutionMs != (15 * time.Minute).Milliseconds() {
		t.Fatalf("execution = %v, want 15m", time.Duration(got.ExecutionMs)*time.Millisecond)
	}
	if got.WallMs != (24*time.Hour + 5*time.Minute).Milliseconds() {
		t.Fatalf("wall = %v", time.Duration(got.WallMs)*time.Millisecond)
	}
	if got.WaitingMs != got.WallMs-got.ExecutionMs || got.Executing {
		t.Fatalf("unexpected timing %+v", got)
	}
}

func TestRunTimingExcludesHumanGateAndPause(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	events := timingEvents(t0,
		time.Duration(0), "review", "node.started",
		2*time.Minute, "review", "gate.waiting",
		5*time.Hour, "review", "node.succeeded",
		5*time.Hour, "test", "node.started",
		5*time.Hour+time.Minute, "test", "node.paused",
		5*time.Hour+time.Minute, "", "run.paused",
		8*time.Hour, "test", "node.started",
		8*time.Hour+time.Minute, "test", "node.succeeded",
		8*time.Hour+time.Minute, "", "run.succeeded",
	)
	got := ComputeRunTiming(Run{Status: "succeeded", CreatedAt: t0}, events, t0.Add(10*time.Hour))
	if got.ExecutionMs != (4 * time.Minute).Milliseconds() {
		t.Fatalf("execution = %v, want 4m", time.Duration(got.ExecutionMs)*time.Millisecond)
	}
}

func TestRunTimingUnionsParallelNodesAndClosesOnPark(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	events := timingEvents(t0,
		time.Duration(0), "a", "node.started",
		5*time.Minute, "b", "node.started",
		10*time.Minute, "a", "node.succeeded",
		12*time.Minute, "", "run.parked",
		30*time.Minute, "b", "node.started",
		33*time.Minute, "b", "node.succeeded",
		33*time.Minute, "", "run.succeeded",
	)
	got := ComputeRunTiming(Run{Status: "succeeded", CreatedAt: t0}, events, t0.Add(time.Hour))
	if got.ExecutionMs != (15 * time.Minute).Milliseconds() {
		t.Fatalf("execution = %v, want 15m", time.Duration(got.ExecutionMs)*time.Millisecond)
	}
}

func TestRunTimingRunningNodeAccruesUntilNow(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	now := t0.Add(10 * time.Minute)
	events := timingEvents(t0, 7*time.Minute, "a", "node.started")
	got := ComputeRunTiming(Run{Status: "running", CreatedAt: t0}, events, now)
	if got.ExecutionMs != (3*time.Minute).Milliseconds() || !got.Executing || got.WallMs != (10*time.Minute).Milliseconds() {
		t.Fatalf("unexpected timing %+v", got)
	}

	waiting := ComputeRunTiming(Run{Status: "waiting", CreatedAt: t0}, timingEvents(t0,
		time.Duration(0), "a", "node.started", 2*time.Minute, "a", "gate.waiting"), t0.Add(48*time.Hour))
	if waiting.ExecutionMs != (2*time.Minute).Milliseconds() || waiting.Executing || waiting.WallMs != (48*time.Hour).Milliseconds() {
		t.Fatalf("waiting run timing %+v", waiting)
	}
}

func TestRunTimingFallsBackToNodeRows(t *testing.T) {
	t0 := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	run := Run{Status: "succeeded", CreatedAt: t0, UpdatedAt: t0.Add(time.Hour), Nodes: map[string]*NodeRun{
		"a": {Status: "succeeded", StartedAt: t0, FinishedAt: t0.Add(4 * time.Minute)},
		"b": {Status: "succeeded", StartedAt: t0.Add(50 * time.Minute), FinishedAt: t0.Add(51 * time.Minute)},
	}}
	got := ComputeRunTiming(run, nil, t0.Add(2*time.Hour))
	if got.ExecutionMs != (5*time.Minute).Milliseconds() || got.WallMs != time.Hour.Milliseconds() {
		t.Fatalf("unexpected timing %+v", got)
	}
}

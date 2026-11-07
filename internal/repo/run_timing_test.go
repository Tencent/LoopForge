package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// 列表只带节点最近一次尝试的时间；失败后隔天重试，墙钟是一天多，
// 真正执行只有两次尝试加起来的时长。
func TestRunSummariesAttachEventTiming(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	t0 := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Millisecond)
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	run := domain.Run{
		ID: "run-timed", Status: "succeeded", CreatedAt: t0,
		Nodes: map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded", StartedAt: at(24 * time.Hour), FinishedAt: at(24*time.Hour + 5*time.Minute)}},
		Events: []domain.RunEvent{
			{ID: "e0", NodeID: "code", Type: "node.started", Timestamp: at(0)},
			{ID: "e1", NodeID: "code", Type: "node.failed", Timestamp: at(10 * time.Minute)},
			{ID: "e2", Type: "run.failed", Timestamp: at(10 * time.Minute)},
			{ID: "e3", Type: "run.resumed", Timestamp: at(time.Hour)},
			{ID: "e4", NodeID: "code", Type: "node.started", Timestamp: at(24 * time.Hour)},
			{ID: "e5", NodeID: "code", Type: "node.succeeded", Timestamp: at(24*time.Hour + 5*time.Minute)},
			{ID: "e6", Type: "run.succeeded", Timestamp: at(24*time.Hour + 5*time.Minute)},
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	runs, err := s.RunSummaries()
	if err != nil || len(runs) != 1 || runs[0].Timing == nil {
		t.Fatalf("summaries = %+v, err = %v", runs, err)
	}
	timing := runs[0].Timing
	if timing.ExecutionMs != (15 * time.Minute).Milliseconds() {
		t.Fatalf("execution = %v, want 15m", time.Duration(timing.ExecutionMs)*time.Millisecond)
	}
	if timing.WallMs != (24*time.Hour + 5*time.Minute).Milliseconds() {
		t.Fatalf("wall = %v", time.Duration(timing.WallMs)*time.Millisecond)
	}
	if got := runExecutionDuration(runs[0]); got != timing.ExecutionMs {
		t.Fatalf("analytics execution %d must follow event timing %d", got, timing.ExecutionMs)
	}
	view, ok := s.RunView(run.ID)
	if !ok || view.Timing == nil || view.Timing.ExecutionMs != timing.ExecutionMs || view.Timing.WallMs != timing.WallMs {
		t.Fatalf("detail view timing %+v must match the list %+v", view.Timing, timing)
	}
}

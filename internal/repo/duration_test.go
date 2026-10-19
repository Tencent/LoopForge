package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRunDurationPrefersLastNodeFinish(t *testing.T) {
	created := time.Date(2026, 8, 17, 16, 6, 46, 0, time.UTC)
	closeAt := time.Date(2026, 8, 17, 17, 40, 17, 0, time.UTC)
	restoredAt := time.Date(2026, 8, 18, 4, 21, 0, 0, time.UTC)
	run := domain.Run{
		Status:    "succeeded",
		CreatedAt: created,
		UpdatedAt: restoredAt,
		Nodes: map[string]*domain.NodeRun{
			"analyze": {FinishedAt: created.Add(7 * time.Minute)},
			"close":   {FinishedAt: closeAt},
		},
	}
	got := runDuration(run, restoredAt)
	want := closeAt.Sub(created).Milliseconds()
	if got != want {
		t.Fatalf("duration=%d want %d (close-created), not restore-created %d", got, want, restoredAt.Sub(created).Milliseconds())
	}
}

func TestRunExecutionDurationSumsNodeTime(t *testing.T) {
	created := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	run := domain.Run{
		Status:    "succeeded",
		CreatedAt: created,
		UpdatedAt: created.Add(6 * time.Hour),
		Nodes: map[string]*domain.NodeRun{
			"analyze": {StartedAt: created.Add(time.Hour), FinishedAt: created.Add(70 * time.Minute)},
			// 缺少结束时间的节点不计入执行耗时。
			"running": {StartedAt: created.Add(2 * time.Hour)},
			// 倒挂时间戳的节点同样跳过。
			"broken": {StartedAt: created.Add(3 * time.Hour), FinishedAt: created.Add(2 * time.Hour)},
			"close":  {StartedAt: created.Add(4 * time.Hour), FinishedAt: created.Add(4*time.Hour + 5*time.Minute)},
		},
	}
	got := runExecutionDuration(run)
	want := int64(15 * time.Minute / time.Millisecond)
	if got != want {
		t.Fatalf("execution duration=%d want %d", got, want)
	}
	// 执行耗时只累加节点耗时，因此明显小于端到端墙钟。
	if wall := runDuration(run, created.Add(6*time.Hour)); wall <= got {
		t.Fatalf("wall duration=%d should exceed execution duration=%d", wall, got)
	}
}

func TestRunWaitDurationExcludesExecution(t *testing.T) {
	created := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	lastFinished := created.Add(3 * time.Hour)
	run := domain.Run{
		Status:    "succeeded",
		CreatedAt: created,
		Nodes: map[string]*domain.NodeRun{
			"work": {StartedAt: created.Add(2 * time.Hour), FinishedAt: lastFinished},
		},
	}
	// 等待 2 小时 + 执行 1 小时 = 端到端 3 小时。
	if got, want := runExecutionDuration(run), int64(time.Hour/time.Millisecond); got != want {
		t.Fatalf("execution duration=%d want %d", got, want)
	}
	if got, want := runWaitDuration(run, lastFinished), int64(2*time.Hour/time.Millisecond); got != want {
		t.Fatalf("wait duration=%d want %d", got, want)
	}
}

func TestRunWaitDurationMergesParallelNodeIntervals(t *testing.T) {
	created := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	finished := created.Add(10 * time.Minute)
	run := domain.Run{Status: "succeeded", CreatedAt: created, Nodes: map[string]*domain.NodeRun{
		"a": {StartedAt: created.Add(5 * time.Minute), FinishedAt: finished},
		"b": {StartedAt: created.Add(5 * time.Minute), FinishedAt: finished},
	}}
	if got, want := runExecutionDuration(run), int64(10*time.Minute/time.Millisecond); got != want {
		t.Fatalf("summed execution duration=%d want %d", got, want)
	}
	if got, want := runActiveDuration(run, finished), int64(5*time.Minute/time.Millisecond); got != want {
		t.Fatalf("active duration=%d want %d", got, want)
	}
	if got, want := runWaitDuration(run, finished), int64(5*time.Minute/time.Millisecond); got != want {
		t.Fatalf("wait duration=%d want %d", got, want)
	}
}

func TestUsageCostKnown(t *testing.T) {
	if !usageCostKnown(domain.TokenUsage{CostStatus: "reported"}) {
		t.Fatal("reported usage should be known")
	}
	if usageCostKnown(domain.TokenUsage{CostStatus: "unknown", CostUSD: 1}) {
		t.Fatal("explicit unknown should stay unknown even with amount")
	}
	// 旧数据没有 costStatus：有金额视为已识别，零金额视为未识别。
	if !usageCostKnown(domain.TokenUsage{CostUSD: 0.5}) {
		t.Fatal("legacy usage with amount should be known")
	}
	if usageCostKnown(domain.TokenUsage{}) {
		t.Fatal("legacy usage without amount should be unknown")
	}
}

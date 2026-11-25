package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// TestAnalyticsScheduleExecutionAttribution verifies that the schedule
// attribution written by #1187 reaches analytics traces and is aggregated into
// a dedicated, non-inflating breakdown. Manual/legacy runs stay empty and must
// not participate in the schedule breakdown.
func TestAnalyticsScheduleExecutionAttribution(t *testing.T) {
	wf := domain.WorkflowDefinition{ID: "sop", Name: "Scheduled SOP", Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build"}}}
	now := time.Now().UTC().Add(-time.Minute)
	ended := func(status string) map[string]*domain.NodeRun {
		return map[string]*domain.NodeRun{"build": {NodeID: "build", Status: status, StartedAt: now, FinishedAt: now.Add(time.Second)}}
	}
	runs := []domain.Run{
		{ID: "sched-1", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", ScheduleJobID: "job-a", ScheduleExecutionID: "sjx-a1"},
			Nodes:            ended("succeeded"), Usage: domain.TokenUsage{TotalTokens: 30, CostUSD: 0.03}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		{ID: "sched-2", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", ScheduleJobID: "job-a", ScheduleExecutionID: "sjx-a2"},
			Nodes:            ended("succeeded"), Usage: domain.TokenUsage{TotalTokens: 20, CostUSD: 0.02}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		{ID: "sched-other", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed",
			ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", ScheduleJobID: "job-b", ScheduleExecutionID: "sjx-b1"},
			Nodes:            ended("failed"), Usage: domain.TokenUsage{TotalTokens: 10, CostUSD: 0.01}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		{ID: "manual", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "alpha"},
			Nodes:            ended("succeeded"), Usage: domain.TokenUsage{TotalTokens: 100, CostUSD: 0.10}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
	}

	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	filter := domain.AnalyticsFilter{ProjectID: "alpha"}
	out := s.AnalyticsRepo.analyticsWithRuns(filter, runs)

	// Every trace keeps its own attribution; manual runs stay empty.
	byID := map[string]domain.AnalyticsRunTrace{}
	for _, trace := range out.RunTraces {
		byID[trace.RunID] = trace
	}
	for id, want := range map[string]struct{ job string }{
		"sched-1": {job: "job-a"}, "sched-2": {job: "job-a"}, "sched-other": {job: "job-b"},
	} {
		trace, ok := byID[id]
		if !ok {
			t.Fatalf("missing trace for %s", id)
		}
		if trace.ScheduleJobID != want.job {
			t.Errorf("%s scheduleJobId = %q, want %q", id, trace.ScheduleJobID, want.job)
		}
		// The exact execution differs per occurrence; just require it present.
		if trace.ScheduleExecutionID == "" {
			t.Errorf("%s lost scheduleExecutionId", id)
		}
	}
	if got := byID["manual"].ScheduleJobID; got != "" {
		t.Errorf("manual run scheduleJobId = %q, want empty", got)
	}
	if got := byID["manual"].ScheduleExecutionID; got != "" {
		t.Errorf("manual run scheduleExecutionId = %q, want empty", got)
	}

	// The breakdown contains only the two scheduled jobs. job-a has 2 runs
	// (30+20 tokens), job-b has 1 (10 tokens). Manual tokens never leak in.
	if len(out.ScheduleJobs) != 2 {
		t.Fatalf("schedule breakdown count = %d, want 2: %+v", len(out.ScheduleJobs), out.ScheduleJobs)
	}
	byJob := map[string]domain.AnalyticsBreakdown{}
	for _, b := range out.ScheduleJobs {
		byJob[b.Key] = b
	}
	if a := byJob["job-a"]; a.Runs != 2 || a.Tokens != 50 || a.Succeeded != 2 {
		t.Errorf("job-a breakdown = %+v, want runs=2 tokens=50 succeeded=2", a)
	}
	if b := byJob["job-b"]; b.Runs != 1 || b.Tokens != 10 || b.Failed != 1 {
		t.Errorf("job-b breakdown = %+v, want runs=1 tokens=10 failed=1", b)
	}

	// The dedicated breakdown must not change overall totals: all four runs
	// (including the manual one) are still counted exactly once.
	if out.Summary.TotalRuns != 4 || out.Summary.TotalTokens != 160 {
		t.Errorf("schedule breakdown inflated overall totals: totalRuns=%d totalTokens=%d", out.Summary.TotalRuns, out.Summary.TotalTokens)
	}

	// Legacy records without any execution context stay out of the breakdown.
	legacy := []domain.Run{{ID: "legacy", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "alpha"}, Nodes: ended("succeeded"), CreatedAt: now, UpdatedAt: now.Add(time.Second)}}
	outLegacy := s.AnalyticsRepo.analyticsWithRuns(filter, legacy)
	if len(outLegacy.ScheduleJobs) != 0 {
		t.Errorf("legacy runs produced a schedule breakdown: %+v", outLegacy.ScheduleJobs)
	}
}

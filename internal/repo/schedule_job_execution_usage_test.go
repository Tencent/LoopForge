package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestScheduleJobExecutionUsageOverview(t *testing.T) {
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			st := openExecutionTestStore(t, backend)
			jobID := fmt.Sprintf("usage-overview-%d", time.Now().UnixNano())
			if err := st.PutScheduleJob(domain.ScheduleJob{ID: jobID, ProjectID: "platform", Name: "AI", SkipDispatch: true, AssistantPrompt: "inspect"}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.DeleteScheduleJob(jobID) })
			now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
			put := func(trace string, started time.Time, status string, usage *domain.TokenUsage) {
				t.Helper()
				e := domain.ScheduleJobExecution{JobID: jobID, ProjectID: "platform", TraceID: jobID + trace, Status: "running", StartedAt: started}
				if err := st.PutScheduleJobExecution(e); err != nil {
					t.Fatal(err)
				}
				e.Status, e.DurationMs = status, 1000
				e.RecordUsage("unpriced-test-model", usage)
				for range 2 {
					if err := st.PutScheduleJobExecution(e); err != nil {
						t.Fatal(err)
					}
				}
			}
			put("success", now.Add(-2*time.Hour), "success", &domain.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 20, CacheReadTokens: 100, CacheWriteTokens: 3})
			put("failed", now.Add(-time.Hour), "failed", &domain.TokenUsage{InputTokens: 20, OutputTokens: 10, CacheReadTokens: 200, CacheWriteTokens: 6})
			put("unreported", now.Add(-time.Hour), "success", nil)
			put("cache-only-failed", now.Add(-time.Hour), "failed", &domain.TokenUsage{CacheReadTokens: 40000})
			put("previous", now.Add(-200*time.Hour), "success", &domain.TokenUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6})
			if err := st.PutScheduleJobExecution(domain.ScheduleJobExecution{JobID: jobID, ProjectID: "platform", TraceID: jobID + "running", Status: "running", StartedAt: now}); err != nil {
				t.Fatal(err)
			}
			current, previous := st.ScheduleJobExecutionOverview([]string{jobID}, now)
			wantCurrent := domain.ScheduleExecutionWindow{
				Total: 5, Finished: 4, Success: 2, Failed: 2, Running: 1, AvgMs: 1000,
				Metered: 3, AvgTokens: 16,
				Tokens: domain.ScheduleExecutionTokens{Total: 50, Input: 30, Output: 15, CacheRead: 40300, CacheWrite: 9},
			}
			wantPrevious := domain.ScheduleExecutionWindow{
				Total: 1, Finished: 1, Success: 1, AvgMs: 1000, Metered: 1, AvgTokens: 6,
				Tokens: domain.ScheduleExecutionTokens{Total: 6, Input: 4, Output: 2},
			}
			if current != wantCurrent || previous != wantPrevious {
				t.Fatalf("overview=%+v / %+v, want=%+v / %+v", current, previous, wantCurrent, wantPrevious)
			}
			if err := st.DB.Where("job_id = ?", jobID).Delete(&scheduleJobExecutionRow{}).Error; err != nil {
				t.Fatal(err)
			}
			current, _ = st.ScheduleJobExecutionOverview([]string{jobID}, now)
			wantCurrent.Total, wantCurrent.Running = 4, 0
			if current != wantCurrent {
				t.Fatalf("retention changed usage overview: %+v", current)
			}
		})
	}
}

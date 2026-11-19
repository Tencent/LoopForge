package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Runs on MySQL too: gorm renders Offset without Limit as a bare OFFSET,
// which MySQL rejects, and the failed prune rolled back every write.
func TestScheduleJobExecutionsPruneAndFollowJobDelete(t *testing.T) {
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			var st *Store
			var err error
			if backend == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
				}
				st, err = Open(dsn)
			} else {
				st, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			suffix := time.Now().UnixNano()
			jobID, otherID := fmt.Sprintf("exec-job-%d", suffix), fmt.Sprintf("exec-other-%d", suffix)
			trace := func(i int) string { return fmt.Sprintf("%d-%03d", suffix, i) }
			if err := st.PutScheduleJob(domain.ScheduleJob{ID: jobID, Name: "AI", ProjectID: "platform", SkipDispatch: true, AssistantPrompt: "p"}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = st.DeleteScheduleJob(jobID)
				_ = st.DB.Where("job_id = ?", otherID).Delete(&scheduleJobExecutionRow{}).Error
			})
			base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			total := MaxScheduleJobExecutionsPerJob + 5
			for i := range total {
				e := domain.ScheduleJobExecution{JobID: jobID, ProjectID: "platform", TraceID: trace(i), Status: "running", StartedAt: base.Add(time.Duration(i) * time.Minute)}
				if err := st.PutScheduleJobExecution(e); err != nil {
					t.Fatal(err)
				}
			}
			// Finishing an execution rewrites the same row.
			finished := base.Add(time.Duration(total) * time.Minute)
			last := domain.ScheduleJobExecution{JobID: jobID, ProjectID: "platform", TraceID: trace(total - 1), Status: "failed", Error: "boom", StartedAt: base.Add(time.Duration(total-1) * time.Minute), FinishedAt: &finished}
			if err := st.PutScheduleJobExecution(last); err != nil {
				t.Fatal(err)
			}
			if err := st.PutScheduleJobExecution(domain.ScheduleJobExecution{JobID: otherID, TraceID: trace(999), Status: "failed", StartedAt: base}); err != nil {
				t.Fatal(err)
			}
			got := st.ScheduleJobExecutions(ScheduleJobExecutionQuery{JobIDs: []string{jobID}, Limit: 500})
			if len(got) != MaxScheduleJobExecutionsPerJob {
				t.Fatalf("kept %d executions", len(got))
			}
			if got[0].TraceID != trace(total-1) || got[0].Status != "failed" || got[0].Error != "boom" || got[len(got)-1].TraceID != trace(5) {
				t.Fatalf("order, update or prune wrong: first=%+v last=%s", got[0], got[len(got)-1].TraceID)
			}
			if byTrace := st.ScheduleJobExecutions(ScheduleJobExecutionQuery{TraceID: trace(total - 1)}); len(byTrace) != 1 {
				t.Fatalf("trace lookup = %+v", byTrace)
			}
			if none := st.ScheduleJobExecutions(ScheduleJobExecutionQuery{JobIDs: []string{}}); len(none) != 0 {
				t.Fatalf("empty job set returned %d", len(none))
			}
			if err := st.DeleteScheduleJob(jobID); err != nil {
				t.Fatal(err)
			}
			if left := st.ScheduleJobExecutions(ScheduleJobExecutionQuery{JobIDs: []string{jobID, otherID}, Limit: 500}); len(left) != 1 || left[0].JobID != otherID {
				t.Fatalf("executions after delete = %+v", left)
			}
		})
	}
}

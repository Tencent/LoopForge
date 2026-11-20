package schedule

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

func TestExecutionStartFailureStopsExternalActions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ai    bool
		async bool
	}{
		{name: "AI", ai: true},
		{name: "SOP"},
		{name: "async AI", ai: true, async: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			job := domain.ScheduleJob{ID: "job", ProjectID: "platform", WorkflowID: "wf", Enabled: true, SkipDispatch: tc.ai}
			if tc.ai {
				job.AssistantPrompt = "inspect"
			}
			if err := st.PutScheduleJob(job); err != nil {
				t.Fatal(err)
			}
			if err := st.PutIssue(domain.Issue{ID: "issue", ProjectID: "platform", Status: "todo"}); err != nil {
				t.Fatal(err)
			}
			// Only the execution insert fails; job claim/result writes still work.
			if err := st.DB.Exec(`CREATE TRIGGER reject_execution_start BEFORE INSERT ON schedule_job_executions
				BEGIN SELECT RAISE(FAIL, 'execution ledger unavailable'); END`).Error; err != nil {
				t.Fatal(err)
			}
			svc := New(st, tracker.NewService(), nil)
			var calls atomic.Int64
			svc.SetAssistantPrep(func(domain.ScheduleJob) (string, error) {
				calls.Add(1)
				return "reply", nil
			})
			svc.SetRunDispatcher(func(_ domain.ScheduleJob, issue domain.Issue, _ string) (string, error) {
				calls.Add(1)
				return "run-" + issue.ID, nil
			})
			var result domain.ScheduleJob
			if tc.async {
				started, err := svc.StartNow(job.ID)
				if err != nil || started.LastStatus != "running" {
					t.Fatalf("asynchronous acceptance changed: job=%+v err=%v", started, err)
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					result, _ = st.ScheduleJob(job.ID)
					if result.LastStatus != "running" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("asynchronous execution did not finish")
					}
					time.Sleep(10 * time.Millisecond)
				}
			} else {
				result, err = svc.RunNow(job.ID)
				if err == nil {
					t.Error("synchronous execution swallowed the ledger failure")
				} else if strings.Contains(err.Error(), "execution ledger unavailable") {
					t.Errorf("execution exposed the storage error: %v", err)
				}
			}
			if calls.Load() != 0 || result.LastStatus != "failed" || result.LastPulled != 0 || result.LastDispatched != 0 {
				t.Fatalf("unrecorded execution performed work: calls=%d job=%+v", calls.Load(), result)
			}
			if result.LastError == "" || strings.Contains(result.LastError, "execution ledger unavailable") {
				t.Fatalf("missing or unsanitized failure reason: %q", result.LastError)
			}
			if _, exists := st.ScheduleJobExecution(domain.ScheduleJobExecutionID(result.LastTraceID)); exists {
				t.Fatal("rejected execution unexpectedly exists")
			}
			if !tc.async {
				if err := st.DB.Exec("DROP TRIGGER reject_execution_start").Error; err != nil {
					t.Fatal(err)
				}
				retried, err := svc.RunNow(job.ID)
				if err != nil || retried.LastStatus != "success" || calls.Load() != 1 {
					t.Fatalf("execution did not recover after storage recovered: calls=%d job=%+v err=%v", calls.Load(), retried, err)
				}
			}
		})
	}
}

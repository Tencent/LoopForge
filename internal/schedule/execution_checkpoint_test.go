package schedule

import (
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

func TestExecutionCheckpointsDispatchedRunsBeforeNextDispatch(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, id := range []string{"issue-a", "issue-b"} {
		if err := st.PutIssue(domain.Issue{ID: id, Title: id, ProjectID: "platform", Status: "todo"}); err != nil {
			t.Fatal(err)
		}
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", WorkflowID: "wf", Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	svc := New(st, tracker.NewService(), nil)
	blocked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	calls := 0
	var firstIssue string
	svc.SetRunDispatcher(func(_ domain.ScheduleJob, issue domain.Issue, _ string) (string, error) {
		calls++
		if calls == 1 {
			firstIssue = issue.ID
		} else {
			close(blocked)
			<-release
		}
		return "run-" + issue.ID, nil
	})
	go func() {
		_, err := svc.RunNow(job.ID)
		done <- err
	}()
	t.Cleanup(func() {
		close(release)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("dispatch completion: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("dispatch did not stop")
		}
	})
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("second dispatch did not start")
	}
	stored, _ := st.ScheduleJob(job.ID)
	id := domain.ScheduleJobExecutionID(stored.LastTraceID)
	execution, ok := st.ScheduleJobExecution(id)
	if !ok || execution.Status != "running" || len(execution.Runs) != 1 || execution.Runs[0].RunID != "run-"+firstIssue || execution.Pulled != 2 || execution.Dispatched != 1 {
		t.Fatalf("running execution lost a successful dispatch: %+v", execution)
	}
	// Recovery reads only durable execution data, as a successor process would.
	if err := st.FailRunningScheduleJobExecutions(time.Now().UTC().Add(time.Second), interruptedReason); err != nil {
		t.Fatal(err)
	}
	recovered, _ := st.ScheduleJobExecution(id)
	if recovered.Status != "failed" || len(recovered.Runs) != 1 || recovered.Runs[0].RunID != execution.Runs[0].RunID || recovered.Dispatched != 1 {
		t.Fatalf("recovery lost dispatched run attribution: %+v", recovered)
	}
}

func TestExecutionStopsDispatchingWhenRunCheckpointFails(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, id := range []string{"issue-a", "issue-b"} {
		if err := st.PutIssue(domain.Issue{ID: id, ProjectID: "platform", Status: "todo"}); err != nil {
			t.Fatal(err)
		}
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", WorkflowID: "wf", Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	// The initial execution insert and terminal failure update still work.
	if err := st.DB.Exec(`CREATE TRIGGER reject_run_checkpoint BEFORE UPDATE ON schedule_job_executions
		WHEN NEW.status = 'running' BEGIN SELECT RAISE(FAIL, 'checkpoint unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	svc := New(st, tracker.NewService(), nil)
	calls := 0
	svc.SetRunDispatcher(func(_ domain.ScheduleJob, issue domain.Issue, _ string) (string, error) {
		calls++
		return "run-" + issue.ID, nil
	})
	result, err := svc.RunNow(job.ID)
	if err == nil || calls != 1 || result.LastStatus != "failed" || result.LastDispatched != 1 {
		t.Fatalf("checkpoint failure allowed further dispatch: calls=%d result=%+v err=%v", calls, result, err)
	}
	execution, _ := st.ScheduleJobExecution(domain.ScheduleJobExecutionID(result.LastTraceID))
	if execution.Status != "failed" || execution.Dispatched != 1 || len(execution.Runs) != 1 || execution.Runs[0].RunID == "" {
		t.Fatalf("failed checkpoint lost the completed dispatch: %+v", execution)
	}
}

func TestExecutionCheckpointsCountOnlyTheFinishedExecution(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, id := range []string{"issue-a", "issue-b"} {
		if err := st.PutIssue(domain.Issue{ID: id, ProjectID: "platform", Status: "todo"}); err != nil {
			t.Fatal(err)
		}
	}
	job := domain.ScheduleJob{ID: "job", ProjectID: "platform", WorkflowID: "wf", Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	svc := New(st, tracker.NewService(), nil)
	svc.SetRunDispatcher(func(_ domain.ScheduleJob, issue domain.Issue, _ string) (string, error) {
		return "run-" + issue.ID, nil
	})
	result, err := svc.RunNow(job.ID)
	if err != nil || result.LastDispatched != 2 {
		t.Fatalf("dispatch result = %+v, %v", result, err)
	}
	current, _ := st.ScheduleJobExecutionOverview([]string{job.ID}, time.Now().UTC())
	if current.Total != 1 || current.Finished != 1 || current.Success != 1 || current.Running != 0 {
		t.Fatalf("intermediate checkpoints affected execution counts: %+v", current)
	}
}

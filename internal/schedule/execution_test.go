package schedule

import (
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

func TestExecutionsRecordEachRunWithTrace(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, id := range []string{"issue-a", "issue-b"} {
		if err := st.PutIssue(domain.Issue{ID: id, Title: "title " + id, ProjectID: "platform", Status: "todo"}); err != nil {
			t.Fatal(err)
		}
	}
	job := domain.ScheduleJob{ID: "job", Name: "触发配置", ProjectID: "platform", WorkflowID: "wf", Enabled: true}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	fail := false
	svc := New(st, tracker.NewService(), nil)
	svc.SetRunDispatcher(func(_ domain.ScheduleJob, issue domain.Issue, _ string) (string, error) {
		if fail {
			return "", errors.New("engine down")
		}
		if err := st.PutTask(domain.Task{ID: "task-" + issue.ID, IssueID: issue.ID, WorkflowID: "wf", Status: "queued"}); err != nil {
			return "", err
		}
		return "run-" + issue.ID, nil
	})

	first, err := svc.RunNow(job.ID)
	if err != nil || first.LastStatus != "success" || first.LastTraceID == "" {
		t.Fatalf("first run = %+v, %v", first, err)
	}
	if err := st.PutIssue(domain.Issue{ID: "issue-c", ProjectID: "platform", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	fail = true
	second, err := svc.RunJob(job.ID)
	if err == nil || second.LastStatus != "failed" {
		t.Fatalf("second run = %+v, %v", second, err)
	}
	if second.LastTraceID == "" || second.LastTraceID == first.LastTraceID {
		t.Fatalf("each execution needs its own trace: %q %q", first.LastTraceID, second.LastTraceID)
	}

	got := st.ScheduleJobExecutions(repo.ScheduleJobExecutionQuery{JobIDs: []string{job.ID}})
	if len(got) != 2 {
		t.Fatalf("executions = %+v", got)
	}
	failed, ok := got[0], got[1]
	if failed.TraceID != second.LastTraceID || failed.Status != "failed" || failed.Error != "engine down" || failed.TriggerOrigin != "timer" || failed.FinishedAt == nil || failed.JobKind != "trigger" {
		t.Fatalf("failed execution = %+v", failed)
	}
	if ok.TraceID != first.LastTraceID || ok.Status != "success" || ok.Dispatched != 2 || ok.TriggerOrigin != "manual_run" || len(ok.Runs) != 2 || ok.Runs[0].RunID == "" || ok.Runs[0].IssueTitle == "" {
		t.Fatalf("success execution = %+v", ok)
	}
	if byTrace := st.ScheduleJobExecutions(repo.ScheduleJobExecutionQuery{TraceID: first.LastTraceID}); len(byTrace) != 1 || byTrace[0].ID != ok.ID {
		t.Fatalf("trace lookup = %+v", byTrace)
	}
	if onlyFailed := st.ScheduleJobExecutions(repo.ScheduleJobExecutionQuery{JobIDs: []string{job.ID}, Status: "failed"}); len(onlyFailed) != 1 {
		t.Fatalf("status filter = %+v", onlyFailed)
	}
}

func TestRecoverFailsExecutionsFromStoppedProcess(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	before := time.Now().UTC().Add(-time.Minute)
	job := domain.ScheduleJob{ID: "job", Name: "AI", ProjectID: "platform", SkipDispatch: true, AssistantPrompt: "p", Enabled: true, LastStatus: "running", LastRunAt: before, LastTraceID: "trace-old"}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	for _, e := range []domain.ScheduleJobExecution{
		{JobID: "job", TraceID: "trace-old", Status: "running", StartedAt: before},
		{JobID: "job", TraceID: "trace-new", Status: "running", StartedAt: time.Now().UTC().Add(time.Minute)},
	} {
		if err := st.PutScheduleJobExecution(e); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(st, tracker.NewService(), nil)
	svc.startedAt = time.Now().UTC()
	svc.recoverInterruptedJobs()

	old, _ := st.ScheduleJobExecution(domain.ScheduleJobExecutionID("trace-old"))
	if old.Status != "failed" || old.Error != interruptedReason || old.FinishedAt == nil {
		t.Fatalf("interrupted execution = %+v", old)
	}
	if current, _ := st.ScheduleJobExecution(domain.ScheduleJobExecutionID("trace-new")); current.Status != "running" {
		t.Fatalf("execution of this process changed: %+v", current)
	}
	if stored, _ := st.ScheduleJob("job"); stored.LastStatus != "failed" || stored.LastTraceID != "trace-old" {
		t.Fatalf("job = %+v", stored)
	}
}

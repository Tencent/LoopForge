package schedule

import (
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

func TestDeferredRecoveryKeepsCurrentProcessJobsRunning(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	startedAt := time.Now().UTC()
	for _, job := range []domain.ScheduleJob{
		{ID: "old", Name: "old", LastStatus: "running", LastRunAt: startedAt.Add(-time.Hour)},
		{ID: "boundary", Name: "boundary", LastStatus: "running", LastRunAt: startedAt},
		{ID: "current", Name: "current", SkipDispatch: true, AssistantPrompt: "run"},
	} {
		if err := st.PutScheduleJob(job); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(st, tracker.NewService(), nil)
	svc.startedAt = startedAt
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	svc.SetAssistantPrep(func(domain.ScheduleJob) (string, error) {
		close(entered)
		<-release
		return "completed", nil
	})
	go func() {
		defer close(done)
		if _, err := svc.RunNow("current"); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() { close(release); <-done })
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("current process did not start its job")
	}
	// The predecessor exits after this process has already accepted a job.
	svc.recoverInterruptedJobs()
	for _, id := range []string{"current", "boundary"} {
		job, _ := st.ScheduleJob(id)
		if job.LastStatus != "running" || job.LastError != "" {
			t.Errorf("recovery failed current occurrence %s: %+v", id, job)
		}
	}
	job, _ := st.ScheduleJob("current")
	execution, ok := st.ScheduleJobExecution(domain.ScheduleJobExecutionID(job.LastTraceID))
	if !ok || execution.Status != "running" || execution.Error != "" {
		t.Fatalf("current execution = %+v", execution)
	}
	old, _ := st.ScheduleJob("old")
	if old.LastStatus != "failed" || old.LastError != interruptedReason {
		t.Fatalf("predecessor occurrence was not recovered: %+v", old)
	}
}

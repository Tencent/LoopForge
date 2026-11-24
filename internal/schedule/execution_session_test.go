package schedule

import (
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

// A prompt cut short after it opened its conversation (a restart while it
// ran) still leaves the execution and the job linked to that conversation,
// and a fixed job stays pinned to it. Config edits made meanwhile survive.
func TestInterruptedExecutionKeepsItsConversation(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	job := domain.ScheduleJob{ID: "job", Name: "AI", ProjectID: "platform", SkipDispatch: true, AssistantPrompt: "p", Enabled: true, IntervalSec: 3600, SessionMode: domain.ScheduleSessionFixed}
	if err := st.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	svc := New(st, tracker.NewService(), nil)
	opened, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	svc.SetAssistantSessionPrep(func(job domain.ScheduleJob, report func(string)) (AssistantRunResult, error) {
		report("chat-live")
		close(opened)
		<-release
		return AssistantRunResult{SessionID: "chat-live"}, nil
	})
	claimed, err := svc.claim("job")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(done)
		_, _ = svc.execute(claimed, "manual_run")
	}()
	<-opened

	// While the prompt still runs, both records already point at the conversation.
	exec, ok := st.ScheduleJobExecution(domain.ScheduleJobExecutionID(claimed.LastTraceID))
	if !ok || exec.Status != "running" || exec.SessionID != "chat-live" {
		t.Fatalf("running execution = %+v", exec)
	}
	stored, _ := st.ScheduleJob("job")
	if stored.LastSessionID != "chat-live" || stored.FixedSessionID != "chat-live" || stored.LastStatus != "running" {
		t.Fatalf("running job = %+v", stored)
	}

	// An edit during the run is kept by the mid-run session write.
	stored.AssistantPrompt = "edited"
	if err := st.PutScheduleJob(stored); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveScheduleJobRunSession("job", claimed.LastRunAt, "chat-again"); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.ScheduleJob("job"); again.AssistantPrompt != "edited" || again.LastSessionID != "chat-again" {
		t.Fatalf("session write lost the edit: %+v", again)
	}
	// Another occurrence's write is fenced off.
	if err := st.SaveScheduleJobRunSession("job", claimed.LastRunAt.Add(-time.Hour), "chat-stale"); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.ScheduleJob("job"); again.LastSessionID != "chat-again" {
		t.Fatalf("stale occurrence wrote the session: %+v", again)
	}

	// A successor process marks the run interrupted; the link survives.
	successor := New(st, tracker.NewService(), nil)
	successor.startedAt = time.Now().UTC().Add(time.Second)
	successor.recoverInterruptedJobs()
	exec, _ = st.ScheduleJobExecution(domain.ScheduleJobExecutionID(claimed.LastTraceID))
	if exec.Status != "failed" || exec.Error != interruptedReason || exec.SessionID != "chat-live" {
		t.Fatalf("interrupted execution = %+v", exec)
	}
	if after, _ := st.ScheduleJob("job"); after.LastStatus != "failed" || after.LastSessionID != "chat-again" || after.FixedSessionID != "chat-again" {
		t.Fatalf("interrupted job = %+v", after)
	}
	close(release)
	<-done
}

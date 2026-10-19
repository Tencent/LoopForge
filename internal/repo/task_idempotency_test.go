package repo

import (
	"sync"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
)

func TestCreateTaskUsesTaskIDAsAtomicIdempotencyKey(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	first := domain.Task{ID: "task-primary-fixed", IssueID: "issue-1", Status: "queued", DispatchMode: "scheduled", ScheduleJobID: "job-1"}
	stored, created, err := st.CreateTask(first)
	if err != nil || !created || stored.ID != first.ID {
		t.Fatalf("first reservation: created=%t task=%+v err=%v", created, stored, err)
	}
	duplicate, created, err := st.CreateTask(domain.Task{ID: first.ID, IssueID: "issue-1", Status: "queued"})
	if err != nil || created || duplicate.ScheduleJobID != "job-1" {
		t.Fatalf("duplicate reservation: created=%t task=%+v err=%v", created, duplicate, err)
	}
}

func TestCreateTaskConcurrentPrimaryReservationHasOneWinner(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := st.CreateTask(domain.Task{ID: "task-primary-concurrent", IssueID: "issue-1", Status: "queued"})
			if err != nil {
				t.Errorf("CreateTask: %v", err)
				return
			}
			if created {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("reservation winners=%d", winners.Load())
	}
}

func TestTerminalIssueIsPersistentlyIgnoredByAutomation(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutIssue(domain.Issue{ID: "done", ProjectID: "platform", Title: "done", Status: "done"}); err != nil {
		t.Fatal(err)
	}
	issue, _ := st.Issue("done")
	if issue.AutomationState != "ignored" {
		t.Fatalf("automationState=%q", issue.AutomationState)
	}
	issue.Status = "todo"
	issue.AutomationState = ""
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	issue, _ = st.Issue("done")
	if issue.AutomationState != "ignored" {
		t.Fatalf("sync-like update cleared ignored state: %+v", issue)
	}
}

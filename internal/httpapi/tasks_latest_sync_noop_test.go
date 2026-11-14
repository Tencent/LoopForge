package httpapi

import (
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// syncTasks runs every two seconds, so syncIssuesToLatestTask must only write
// issues whose linkage or derived status actually changed. An issue already
// pointing at its latest task with the matching status keeps its UpdatedAt,
// and a task whose issue no longer exists must not resurrect that issue.
func TestSyncIssuesToLatestTaskSkipsUnchangedAndMissingIssues(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := &tasksHandlers{deps: &deps{repo: s}}

	for _, issue := range []domain.Issue{
		{ID: "in-sync", ProjectID: "platform", Title: "in sync", Status: "in_progress", LatestTaskID: "task-run"},
		{ID: "closed-in-sync", ProjectID: "platform", Title: "closed", Status: "closed", LatestTaskID: "task-closed"},
	} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string]domain.Issue{}
	for _, id := range []string{"in-sync", "closed-in-sync"} {
		issue, ok := s.Issue(id)
		if !ok {
			t.Fatalf("issue %s missing", id)
		}
		before[id] = issue
	}

	// Any write after this point would bump UpdatedAt.
	time.Sleep(20 * time.Millisecond)

	h.syncIssuesToLatestTask(map[string]domain.Task{
		"in-sync":        {ID: "task-run", IssueID: "in-sync", Status: "running"},
		"closed-in-sync": {ID: "task-closed", IssueID: "closed-in-sync", Status: "running"},
		"ghost":          {ID: "task-ghost", IssueID: "ghost", Status: "running"},
	})

	for id, prev := range before {
		got, _ := s.Issue(id)
		if got.LatestTaskID != prev.LatestTaskID || got.Status != prev.Status || !got.UpdatedAt.Equal(prev.UpdatedAt) {
			t.Fatalf("issue %s was rewritten although nothing changed: before %+v after %+v", id, prev, got)
		}
	}
	if _, ok := s.Issue("ghost"); ok {
		t.Fatal("a task for a missing issue must not create the issue")
	}
}

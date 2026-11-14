package httpapi

import (
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// syncIssuesToLatestTask must refresh an issue whose latest task is unchanged
// but whose task status moved on, and must leave issues without any task in
// the map untouched (no blank LatestTaskID, no status rewrite).
func TestSyncIssuesToLatestTaskStatusOnlyAndUntracked(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := &tasksHandlers{deps: &deps{repo: s}}

	for _, issue := range []domain.Issue{
		{ID: "same-link", ProjectID: "platform", Title: "status only", Status: "in_progress", LatestTaskID: "task-1"},
		{ID: "no-task", ProjectID: "platform", Title: "untracked", Status: "blocked", LatestTaskID: "task-gone"},
		{ID: "relink", ProjectID: "platform", Title: "relink", Status: "todo", LatestTaskID: "task-old"},
	} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}

	h.syncIssuesToLatestTask(map[string]domain.Task{
		"same-link": {ID: "task-1", IssueID: "same-link", Status: "completed"},
		"relink":    {ID: "task-new", IssueID: "relink", Status: "running"},
	})

	got := func(id string) domain.Issue {
		t.Helper()
		issue, ok := s.Issue(id)
		if !ok {
			t.Fatalf("issue %s missing", id)
		}
		return issue
	}
	if issue := got("same-link"); issue.LatestTaskID != "task-1" || issue.Status != "in_review" {
		t.Fatalf("status-only change not synced: %+v", issue)
	}
	if issue := got("no-task"); issue.LatestTaskID != "task-gone" || issue.Status != "blocked" {
		t.Fatalf("issue without a latest task must be left alone: %+v", issue)
	}
	if issue := got("relink"); issue.LatestTaskID != "task-new" || issue.Status != "in_progress" {
		t.Fatalf("relinked issue = %+v", issue)
	}
}

package schedule

import (
	"slices"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/tracker"
)

func TestEligibleIssuesFiltersUndispatchable(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutTask(domain.Task{ID: "t", IssueID: "has-task", RunID: "r", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, tracker.NewService(), nil)
	matched := []domain.Issue{
		{ID: "ok", Status: "todo"},
		{ID: "has-task", Status: "todo"},
		{ID: "merged", Status: "todo", DeliveryState: "merged"},
		{ID: "gf-closed", Status: "todo", Source: "gongfeng", ExternalRef: &domain.ExternalRef{ExternalState: "closed"}},
		{ID: "done", Status: "done"},
		{ID: "closed", Status: "closed"},
		{ID: "cancelled", Status: "cancelled"},
		{ID: "ignored", Status: "todo", AutomationState: "ignored"},
		{ID: "own-wf", Status: "todo", WorkflowID: "issue-wf"},
	}
	got := func(job domain.ScheduleJob) []string {
		var ids []string
		for _, issue := range svc.eligibleIssues(job, matched) {
			ids = append(ids, issue.ID)
		}
		return ids
	}
	if ids := got(domain.ScheduleJob{WorkflowID: "wf"}); !slices.Equal(ids, []string{"ok", "own-wf"}) {
		t.Fatalf("eligible = %v", ids)
	}
	if ids := got(domain.ScheduleJob{UseIssueWorkflow: true}); !slices.Equal(ids, []string{"own-wf"}) {
		t.Fatalf("issue-workflow eligible = %v", ids)
	}
}

func TestIssueBlocked(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "p", Name: "P", DefaultRepositoryID: "default"}); err != nil {
		t.Fatal(err)
	}
	blocked := map[string]domain.ScheduleJobBlocker{"b": {Reason: "conflict"}, "default": {Reason: "conflict"}}
	tests := []struct {
		issue domain.Issue
		want  bool
	}{
		{domain.Issue{ID: "single-free", RepositoryID: "a"}, false},
		{domain.Issue{ID: "multi-one-blocked", RepositoryIDs: []string{"a", "b"}}, true},
		{domain.Issue{ID: "project-default", ProjectID: "p"}, true},
		{domain.Issue{ID: "no-repo", ProjectID: "missing"}, false},
	}
	for _, tc := range tests {
		if got := issueBlocked(st, tc.issue, blocked); got != tc.want {
			t.Fatalf("%s: blocked=%v want %v", tc.issue.ID, got, tc.want)
		}
		if issueBlocked(st, tc.issue, nil) {
			t.Fatalf("%s: blocked with no blockers", tc.issue.ID)
		}
	}
}

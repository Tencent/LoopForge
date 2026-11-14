package httpapi

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestLatestTaskByIssue(t *testing.T) {
	base := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "orphan", CreatedAt: base.Add(time.Hour)},
		{ID: "a-old", IssueID: "issue-a", CreatedAt: base},
		{ID: "a-new", IssueID: "issue-a", CreatedAt: base.Add(time.Minute)},
		{ID: "a-older", IssueID: "issue-a", CreatedAt: base.Add(-time.Minute)},
		{ID: "b-2", IssueID: "issue-b", CreatedAt: base},
		{ID: "b-9", IssueID: "issue-b", CreatedAt: base},
		{ID: "b-1", IssueID: "issue-b", CreatedAt: base},
	}
	latest := latestTaskByIssue(tasks)
	if len(latest) != 2 {
		t.Fatalf("latest = %+v, want only issue-a and issue-b", latest)
	}
	if _, ok := latest[""]; ok {
		t.Fatalf("task without issue should be skipped: %+v", latest)
	}
	if got := latest["issue-a"].ID; got != "a-new" {
		t.Fatalf("issue-a latest = %q, want a-new (latest CreatedAt)", got)
	}
	if got := latest["issue-b"].ID; got != "b-9" {
		t.Fatalf("issue-b latest = %q, want b-9 (largest ID on CreatedAt tie)", got)
	}
}

func TestLatestTaskByIssueEmpty(t *testing.T) {
	if latest := latestTaskByIssue(nil); len(latest) != 0 {
		t.Fatalf("latest = %+v, want empty", latest)
	}
}

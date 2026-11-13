package httpapi

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestIssueMatchesAssignee(t *testing.T) {
	unassigned := domain.Issue{}
	byUser := domain.Issue{AssigneeUserID: 42}
	byID := domain.Issue{AssigneeID: "Alice"}
	blankID := domain.Issue{AssigneeID: "   "}
	cases := []struct {
		name     string
		item     domain.Issue
		assignee string
		want     bool
	}{
		{"unassigned matches empty", unassigned, "unassigned", true},
		{"unassigned is case-insensitive", unassigned, "UNASSIGNED", true},
		{"unassigned treats blank id as empty", blankID, "unassigned", true},
		{"unassigned rejects user id", byUser, "unassigned", false},
		{"unassigned rejects assignee id", byID, "unassigned", false},
		{"numeric user id", byUser, "42", true},
		{"other numeric user id", byUser, "43", false},
		{"assignee id case-insensitive", byID, "alice", true},
		{"assignee id mismatch", byID, "bob", false},
		{"zero user id is not matched by 0", unassigned, "0", false},
	}
	for _, tc := range cases {
		if got := issueMatchesAssignee(tc.item, tc.assignee); got != tc.want {
			t.Errorf("%s: issueMatchesAssignee(%+v, %q) = %v, want %v", tc.name, tc.item, tc.assignee, got, tc.want)
		}
	}
}

func TestIssueMatchesQuery(t *testing.T) {
	item := domain.Issue{
		ID:          "iss_abc",
		Number:      17,
		Title:       "Fix Login Redirect",
		ExternalRef: &domain.ExternalRef{ExternalID: "TAPD-998"},
	}
	cases := []struct {
		needle string
		want   bool
	}{
		{"login", true},
		{"iss_abc", true},
		{"17", true},
		{"tapd-998", true},
		{"fix redirect", true},
		{"logout", false},
	}
	for _, tc := range cases {
		if got := issueMatchesQuery(item, tc.needle); got != tc.want {
			t.Errorf("issueMatchesQuery(%q) = %v, want %v", tc.needle, got, tc.want)
		}
	}
	if issueMatchesQuery(domain.Issue{Title: "plain"}, "tapd-998") {
		t.Fatal("issue without external ref must not match external id")
	}
}

func TestFilterIssuesCombinesPredicates(t *testing.T) {
	now := time.Now()
	items := []domain.Issue{
		{ID: "a", ProjectID: "p1", Status: "todo", Title: "Login bug", AssigneeID: "alice", UpdatedAt: now.Add(-time.Hour)},
		{ID: "b", ProjectID: "p1", Status: "todo", Title: "Login page", UpdatedAt: now},
		{ID: "c", ProjectID: "p1", Status: "done", Title: "Login done", AssigneeID: "alice", UpdatedAt: now},
		{ID: "d", ProjectID: "p2", Status: "todo", Title: "Login other", AssigneeID: "alice", UpdatedAt: now},
	}
	ids := func(list []domain.Issue) []string {
		out := make([]string, 0, len(list))
		for _, item := range list {
			out = append(out, item.ID)
		}
		return out
	}
	got := ids(filterIssues(items, issueListQuery{ProjectID: "p1", Status: "todo", Q: "  LOGIN "}))
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("project+status+query filter = %v, want [b a]", got)
	}
	got = ids(filterIssues(items, issueListQuery{ProjectID: "p1", Assignee: "ALICE"}))
	if len(got) != 2 || got[0] != "c" || got[1] != "a" {
		t.Fatalf("assignee filter = %v, want [c a]", got)
	}
	got = ids(filterIssues(items, issueListQuery{Assignee: "unassigned", Q: "login"}))
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("unassigned filter = %v, want [b]", got)
	}
}

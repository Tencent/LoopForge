package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestMRMergeIssueMatches(t *testing.T) {
	requirement := domain.Issue{ID: "req-1", ProjectID: "p1", Source: "tapd", Kind: "requirement", AssigneeUserID: 42}
	bug := domain.Issue{ID: "bug-1", ProjectID: "p1", Source: "gongfeng", Kind: "issue"}
	local := domain.Issue{ID: "local-1", ProjectID: "p1", Kind: "requirement"}
	runIssues := map[string]bool{"req-1": true}
	for _, tc := range []struct {
		name      string
		filter    domain.AnalyticsFilter
		issue     domain.Issue
		runIssues map[string]bool
		want      bool
	}{
		{"no filter", domain.AnalyticsFilter{}, requirement, nil, true},
		{"project match", domain.AnalyticsFilter{ProjectID: "p1"}, requirement, nil, true},
		{"project mismatch", domain.AnalyticsFilter{ProjectID: "p2"}, requirement, nil, false},
		{"allowed projects exclude", domain.AnalyticsFilter{ProjectIDs: []string{"p2"}}, requirement, nil, false},
		{"source match", domain.AnalyticsFilter{IssueSource: "TAPD"}, requirement, nil, true},
		{"empty source counts as local", domain.AnalyticsFilter{IssueSource: "local"}, local, nil, true},
		{"source mismatch", domain.AnalyticsFilter{IssueSource: "local"}, requirement, nil, false},
		{"requirements only drops bugs", domain.AnalyticsFilter{RequirementsOnly: true}, bug, nil, false},
		{"bugs kept without requirements only", domain.AnalyticsFilter{}, bug, nil, true},
		{"workflow with matching run", domain.AnalyticsFilter{WorkflowID: "wf"}, requirement, runIssues, true},
		{"workflow without matching run", domain.AnalyticsFilter{WorkflowID: "wf"}, requirement, map[string]bool{}, false},
		{"workflow ignores assignee fallback", domain.AnalyticsFilter{WorkflowID: "wf", UserID: "42"}, requirement, map[string]bool{}, false},
		{"user with matching run", domain.AnalyticsFilter{UserID: "7"}, requirement, runIssues, true},
		{"user falls back to assignee", domain.AnalyticsFilter{UserID: "42"}, requirement, map[string]bool{}, true},
		{"user neither run nor assignee", domain.AnalyticsFilter{UserID: "99"}, requirement, map[string]bool{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mrMergeIssueMatches(tc.filter, tc.issue, tc.runIssues); got != tc.want {
				t.Fatalf("mrMergeIssueMatches=%v want %v", got, tc.want)
			}
		})
	}
}

func TestNewAnalyticsMRMerge(t *testing.T) {
	mergedAt := time.Date(2026, 10, 20, 8, 30, 0, 0, time.FixedZone("CST", 8*3600))
	issue := domain.Issue{ID: "req-1", Title: "Delivery"}
	row := mrDeliveryStateRow{MRProject: "group/repo", MRIID: 3, MRURL: "https://example.test/mr/3", TargetBranch: "main", MergedAt: &mergedAt}

	got := newAnalyticsMRMerge(row, issue)
	want := domain.AnalyticsMRMerge{
		MRProject: "group/repo", MRIID: 3, MRURL: "https://example.test/mr/3",
		TargetBranch: "main", IssueID: "req-1", Title: "Delivery", MergedAt: mergedAt.UTC(),
	}
	if got != want {
		t.Fatalf("record=%+v want %+v", got, want)
	}
	if got.MergedAt.Location() != time.UTC {
		t.Fatalf("merged time must be UTC: %v", got.MergedAt)
	}

	row.MRURL = ""
	if got := newAnalyticsMRMerge(row, issue).MRURL; got != domain.GongfengMRURL("group/repo", 3) || got == "" {
		t.Fatalf("missing URL must fall back to Gongfeng MR URL, got %q", got)
	}
}

func TestMRMergeRunIssuesSkipsQueryWithoutRunFilter(t *testing.T) {
	// A nil repo proves no Run query happens without a workflow/user filter.
	got, err := (&AnalyticsRepo{}).mrMergeRunIssues(domain.AnalyticsFilter{ProjectID: "p1"}, nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got=%v err=%v, want empty set", got, err)
	}
}

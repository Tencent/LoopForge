package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// mrMergeGuardStore seeds one requirement owned by assignee 42 with merged MRs:
// one with a merge time and one still missing it.
func mrMergeGuardStore(t *testing.T) (*Store, domain.Issue, time.Time) {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{ID: "mr-guard-req", ProjectID: "mr-guard", Source: "tapd", Kind: "requirement", Title: "Guarded delivery", AssigneeUserID: 42}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	mergeAt := time.Date(2026, 10, 20, 0, 30, 0, 0, time.UTC)
	for _, mr := range []struct {
		iid int
		at  time.Time
	}{{1, mergeAt}, {2, time.Time{}}} {
		if err := s.EnsureMRDeliveryState("mr-guard/repo", mr.iid, domain.GongfengMRURL("mr-guard/repo", mr.iid), issue.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateMRDeliveryState("mr-guard/repo", mr.iid, "merged", "main", mr.at, mergeAt.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	return s, issue, mergeAt
}

// A workflow filter combined with the issue assignee must keep the workflow's
// Runs even though the Run executor is someone else: mrMergeRunIssues has to
// resolve the user against the Run's issue, not an empty issue.
func TestMRMergeAnalyticsWorkflowWithIssueAssignee(t *testing.T) {
	s, issue, mergeAt := mrMergeGuardStore(t)
	runAt := mergeAt.Add(-30 * 24 * time.Hour)
	if err := s.PutRun(domain.Run{
		ID: "mr-guard-run", WorkflowID: "mr-guard-wf", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: issue.ProjectID, IssueID: issue.ID, ResponsibleUserID: 7},
		CreatedAt:        runAt, UpdatedAt: runAt.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	base := domain.AnalyticsFilter{ProjectID: issue.ProjectID, From: mergeAt.Add(-time.Hour), To: mergeAt.Add(time.Hour), WorkflowID: "mr-guard-wf"}
	for _, tc := range []struct {
		user        string
		wantMerged  int
		wantMissing int
	}{
		{"42", 1, 1}, // issue assignee, Run executed by user 7
		{"7", 1, 1},  // Run executor
		{"99", 0, 0}, // neither
	} {
		t.Run("user "+tc.user, func(t *testing.T) {
			f := base
			f.UserID = tc.user
			got := s.Analytics(f)
			if got.Summary.MergedMRs != tc.wantMerged || got.Summary.MergedMRsMissingTime != tc.wantMissing {
				t.Fatalf("merged=%d missing=%d want %d/%d", got.Summary.MergedMRs, got.Summary.MergedMRsMissingTime, tc.wantMerged, tc.wantMissing)
			}
		})
	}
}

// Merged MR rows whose issue is not in the caller's (already filtered) issue
// set must be ignored, both as records and in the missing-time count, even
// when no other filter would reject the zero-value issue.
func TestMRMergeAnalyticsIgnoresRowsWithoutKnownIssue(t *testing.T) {
	s, issue, _ := mrMergeGuardStore(t)
	items, missing := s.AnalyticsRepo.mrMergeAnalytics(domain.AnalyticsFilter{}, map[string]domain.Issue{})
	if len(items) != 0 || missing != 0 {
		t.Fatalf("unknown issue rows counted: items=%+v missing=%d", items, missing)
	}
	items, missing = s.AnalyticsRepo.mrMergeAnalytics(domain.AnalyticsFilter{}, map[string]domain.Issue{issue.ID: issue})
	if len(items) != 1 || items[0].IssueID != issue.ID || items[0].Title != issue.Title || missing != 1 {
		t.Fatalf("known issue rows: items=%+v missing=%d", items, missing)
	}
}

// When the Run lookup fails, the workflow/user filtered merge report must be
// empty instead of silently falling back to assignee-only matches.
func TestMRMergeAnalyticsRunQueryFailureReturnsNothing(t *testing.T) {
	s, issue, _ := mrMergeGuardStore(t)
	issues := map[string]domain.Issue{issue.ID: issue}
	filter := domain.AnalyticsFilter{UserID: "42"}
	// Precondition: without a failure the assignee fallback reports the MR.
	if items, missing := s.AnalyticsRepo.mrMergeAnalytics(filter, issues); len(items) != 1 || missing != 1 {
		t.Fatalf("assignee fallback precondition: items=%+v missing=%d", items, missing)
	}
	if err := s.DB.Exec("DROP TABLE runs").Error; err != nil {
		t.Fatal(err)
	}
	if got, err := s.AnalyticsRepo.mrMergeRunIssues(filter, issues); err == nil {
		t.Fatalf("Run query failure must surface, got %v", got)
	}
	if items, missing := s.AnalyticsRepo.mrMergeAnalytics(filter, issues); len(items) != 0 || missing != 0 {
		t.Fatalf("Run query failure must yield no merges: items=%+v missing=%d", items, missing)
	}
}

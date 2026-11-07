package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestMRMergeAnalytics(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			var s *Store
			var err error
			if driver == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("local isolated MySQL required")
				}
				s, err = Open(dsn)
			} else {
				s, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			checkMRMergeAnalytics(t, s)
		})
	}
}

func checkMRMergeAnalytics(t *testing.T, s *Store) {
	prefix := fmt.Sprintf("mr-metrics-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, table := range []string{"mr_delivery_states", "run_nodes", "run_events", "runs", "issues"} {
			column := "id"
			if table == "mr_delivery_states" {
				column = "mr_project"
			} else if table == "run_nodes" || table == "run_events" {
				column = "run_id"
			}
			s.DB.Table(table).Where(column+" LIKE ?", prefix+"%").Delete(nil)
		}
	})
	mergeAt := time.Date(2026, 10, 20, 0, 30, 0, 0, time.UTC)
	checkedAt := mergeAt.Add(48 * time.Hour)
	oldRunAt := mergeAt.Add(-60 * 24 * time.Hour)
	issues := []domain.Issue{
		{ID: prefix + "-requirement", ProjectID: prefix, Source: "tapd", Kind: "requirement", Title: "Multi-repository delivery", AssigneeUserID: 42},
		{ID: prefix + "-other-project", ProjectID: prefix + "-other", Source: "tapd", Kind: "requirement"},
		{ID: prefix + "-bug", ProjectID: prefix, Source: "gongfeng", Kind: "issue"},
		{ID: prefix + "-no-run", ProjectID: prefix, Source: "local", Kind: "requirement", AssigneeUserID: 99},
	}
	for _, issue := range issues {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{prefix + "-run-1", prefix + "-run-2"} {
		if err := s.PutRun(domain.Run{
			ID: id, WorkflowID: prefix + "-workflow", Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: prefix, IssueID: issues[0].ID, ResponsibleUserID: 7},
			CreatedAt:        oldRunAt, UpdatedAt: oldRunAt.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	putMR := func(repository string, iid int, issueID, state string, at time.Time) {
		t.Helper()
		if err := s.EnsureMRDeliveryState(repository, iid, domain.GongfengMRURL(repository, iid), issueID); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateMRDeliveryState(repository, iid, state, "agentflow/integration", at, checkedAt); err != nil {
			t.Fatal(err)
		}
	}
	repoA, repoB := prefix+"/frontend", prefix+"/backend"
	putMR(repoA, 1, issues[0].ID, "merged", mergeAt)
	putMR(repoB, 1, issues[0].ID, "merged", mergeAt.Add(time.Hour))
	putMR(repoA, 2, issues[0].ID, "merged", mergeAt.Add(-48*time.Hour))
	putMR(repoA, 3, issues[0].ID, "merged", mergeAt.Add(48*time.Hour))
	putMR(repoA, 4, issues[0].ID, "merged", time.Time{})
	putMR(repoA, 5, issues[0].ID, "opened", time.Time{})
	putMR(repoA, 6, issues[0].ID, "closed", mergeAt)
	putMR(repoA, 7, domain.BaselinePromoteIssueID, "merged", mergeAt)
	putMR(repoA, 8, issues[1].ID, "merged", mergeAt)
	putMR(repoA, 9, issues[2].ID, "merged", mergeAt)
	putMR(repoA, 10, issues[3].ID, "merged", mergeAt)
	// Repeated polling cannot count a second merge or erase its original time.
	putMR(repoA, 1, issues[0].ID, "merged", time.Time{})

	filter := domain.AnalyticsFilter{ProjectID: prefix, From: mergeAt.Add(-time.Hour), To: mergeAt.Add(24 * time.Hour), RequirementsOnly: true}
	got := s.Analytics(filter)
	if got.Summary.TotalRuns != 0 || got.Summary.MergedMRs != 3 || got.Summary.MergedMRsMissingTime != 1 {
		t.Fatalf("merge clock must be independent of Run/poll time: %+v", got.Summary)
	}
	if len(got.MRMerges) != 3 || len(got.Trend) != 1 || got.Trend[0].Date != "2026-10-20" || got.Trend[0].MergedMRs != 3 {
		t.Fatalf("unexpected merge records/trend: %+v / %+v", got.MRMerges, got.Trend)
	}
	for _, mr := range got.MRMerges {
		if mr.MergedAt.Equal(checkedAt) || mr.MRURL == "" || mr.IssueID == "" {
			t.Fatalf("invalid merge evidence: %+v", mr)
		}
	}
	for _, tc := range []struct {
		name string
		edit func(*domain.AnalyticsFilter)
		want int
	}{
		{"workflow from old runs", func(f *domain.AnalyticsFilter) { f.WorkflowID = prefix + "-workflow" }, 2},
		{"unmatched workflow", func(f *domain.AnalyticsFilter) { f.WorkflowID = "missing-workflow" }, 0},
		{"run executor", func(f *domain.AnalyticsFilter) { f.UserID = "7" }, 2},
		{"issue assignee", func(f *domain.AnalyticsFilter) { f.UserID = "42" }, 2},
		{"assignee without run", func(f *domain.AnalyticsFilter) { f.UserID = "99" }, 1},
		{"source", func(f *domain.AnalyticsFilter) { f.IssueSource = "local" }, 1},
		{"include bugs", func(f *domain.AnalyticsFilter) { f.RequirementsOnly = false }, 4},
		{"empty allowed projects", func(f *domain.AnalyticsFilter) { f.ProjectIDs = []string{} }, 0},
		{"different allowed project", func(f *domain.AnalyticsFilter) { f.ProjectIDs = []string{prefix + "-other"} }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := filter
			tc.edit(&f)
			snap := s.Analytics(f)
			if snap.Summary.MergedMRs != tc.want {
				t.Fatalf("merged=%d want %d", snap.Summary.MergedMRs, tc.want)
			}
		})
	}
	for _, grain := range []string{"week", "month"} {
		f := filter
		f.Grain = grain
		snap := s.Analytics(f)
		want := map[string]string{"week": "2026-W43", "month": "2026-10"}[grain]
		if len(snap.Trend) != 1 || snap.Trend[0].Date != want || snap.Trend[0].MergedMRs != 3 {
			t.Fatalf("%s trend=%+v", grain, snap.Trend)
		}
	}

	pending, err := s.MRDeliveryStatesNeedingRefresh(checkedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	needs := map[int]bool{}
	for _, mr := range pending {
		if mr.MRProject == repoA {
			needs[mr.MRIID] = true
		}
	}
	if !needs[4] || !needs[5] || needs[1] || needs[6] {
		t.Fatalf("historical merged MRs need a timestamp refresh: %+v", needs)
	}
	putMR(repoA, 4, issues[0].ID, "merged", mergeAt)
	got = s.Analytics(filter)
	if got.Summary.MergedMRs != 4 || got.Summary.MergedMRsMissingTime != 0 {
		t.Fatalf("refreshed historical MR missing from window: %+v", got.Summary)
	}
}

package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsDashboardDeliveryCompletion(t *testing.T) {
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	old := at.AddDate(0, -1, 0)
	for _, tc := range []struct {
		name, issueState string
		first, second    string
		firstAt, lastAt  *time.Time
		branch           string
		unregistered     bool
		want             int
	}{
		{name: "partial multi-repository", first: "merged", second: "opened", firstAt: &old},
		{name: "last necessary PR in selected month", first: "merged", second: "merged", firstAt: &old, lastAt: &at, want: 1},
		{name: "both PRs in selected month count once", first: "merged", second: "merged", firstAt: &at, lastAt: &at, want: 1},
		{name: "completed before selected month", first: "merged", second: "merged", firstAt: &old, lastAt: &old},
		{name: "baseline is incomplete", first: "merged", second: "merged", firstAt: &old, lastAt: &at, branch: "agentflow/integration"},
		{name: "reopened requirement", issueState: "reopened", first: "merged", second: "merged", firstAt: &old, lastAt: &at},
		{name: "closed replacement is ignored", first: "closed", second: "merged", lastAt: &at, want: 1},
		{name: "missing merge time is not guessed", first: "merged", second: "merged", lastAt: &at},
		{name: "unregistered necessary PR", first: "merged", second: "opened", firstAt: &at, unregistered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			issue := domain.Issue{ID: "completion", ProjectID: "completion", Kind: "requirement", AssigneeUserID: 7, DeliveryState: tc.issueState}
			if tc.unregistered {
				issue.DeliveryMRs = []domain.DeliveryMR{
					{Project: "completion/a", IID: 1, URL: domain.GongfengMRURL("completion/a", 1), State: tc.first},
					{Project: "completion/b", IID: 1, URL: domain.GongfengMRURL("completion/b", 1), State: tc.second},
				}
			}
			if err := s.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			for _, workspace := range []bool{false, true} {
				id, kind := "sop", ""
				if workspace {
					id, kind = "workspace", domain.RunKindWorkspace
				}
				if err := s.PutRun(domain.Run{ID: id, Kind: kind, Status: "succeeded", CreatedAt: old, ExecutionContext: domain.TelemetryContext{ProjectID: issue.ProjectID, IssueID: issue.ID, ResponsibleUserID: 7}}); err != nil {
					t.Fatal(err)
				}
			}
			branch := tc.branch
			if branch == "" {
				branch = "main"
			}
			rows := []mrDeliveryStateRow{
				{MRProject: "completion/a", MRIID: 1, IssueID: issue.ID, State: tc.first, TargetBranch: "main", MergedAt: tc.firstAt},
				{MRProject: "completion/b", MRIID: 1, IssueID: issue.ID, State: tc.second, TargetBranch: branch, MergedAt: tc.lastAt},
			}
			if tc.unregistered {
				rows = rows[:1]
			}
			if err := s.DB.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			filter := domain.AnalyticsFilter{ProjectID: issue.ProjectID, Scope: "dashboard", Grain: "month", UserID: "7",
				From: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC)}
			out := s.Analytics(filter)
			if len(out.DataErrors) > 0 || out.Dashboard.DeliveredIssues != tc.want || out.Dashboard.SOPDeliveredIssues != tc.want || out.Dashboard.VibeDeliveredIssues != 0 {
				t.Fatalf("delivery completion: %+v / %v", out.Dashboard, out.DataErrors)
			}
			if tc.want > 0 && (len(out.Dashboard.Trend) != 1 || out.Dashboard.Trend[0].Date != "2026-11" || out.Dashboard.Trend[0].SOPDelivered != 1) {
				t.Fatalf("delivery belongs to the final necessary merge month: %+v", out.Dashboard.Trend)
			}
		})
	}
}

package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The delivery dashboard labels a platform requirement as <project key>-<number>
// like the requirement list, so each delivery carries its project and number.
func TestAnalyticsDeliveriesCarryProjectAndIssueNumber(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	prefix := fmt.Sprintf("delivery-key-%d", time.Now().UnixNano())
	at := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	if err := s.PutProject(domain.Project{ID: prefix, Key: "SWP", Name: "Delivery key"}); err != nil {
		t.Fatal(err)
	}
	local := domain.Issue{ID: prefix + "-local", ProjectID: prefix, Number: 3, Title: "local requirement", Status: "in_progress"}
	github := domain.Issue{ID: prefix + "-gh", ProjectID: prefix, Number: 1, Source: "github", Kind: "issue", Title: "github issue", Status: "in_progress",
		ExternalRef: &domain.ExternalRef{Source: "github", ExternalID: "9001", ExternalIID: 1, ExternalURL: "https://github.com/o/r/issues/1", ExternalType: "issue"}}
	for _, issue := range []domain.Issue{local, github} {
		if err := s.IssuesRepo.writeIssue(issue); err != nil {
			t.Fatal(err)
		}
		run := domain.Run{ID: issue.ID + "-run", Status: "succeeded", CreatedAt: at, UpdatedAt: at.Add(10 * time.Minute),
			ExecutionContext: domain.TelemetryContext{IssueID: issue.ID, ProjectID: prefix}, Usage: domain.TokenUsage{TotalTokens: 100}}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: prefix, From: at.Add(-time.Hour), To: at.Add(24 * time.Hour)})
	byID := map[string]domain.AnalyticsDelivery{}
	for _, d := range got.Deliveries {
		byID[d.IssueID] = d
	}
	if d := byID[local.ID]; d.ProjectID != prefix || d.IssueNumber != 3 || d.ExternalIID != 0 || d.Source != "local" {
		t.Fatalf("local delivery must carry project and number: %+v", d)
	}
	if d := byID[github.ID]; d.ProjectID != prefix || d.IssueNumber != 1 || d.ExternalIID != 1 || d.Source != "github" {
		t.Fatalf("github delivery keeps its external iid: %+v", d)
	}
}

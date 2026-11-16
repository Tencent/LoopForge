package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The analytics projections read payload JSON through sqlPayloadJSONText; every
// projected column must round-trip from the stored issue payload.
func TestIssueAnalyticsViewsProjectsPayloadJSONColumns(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	started := time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)
	issue := domain.Issue{
		ID: "iss-projection", ProjectID: "alpha", Title: "projection", Source: "tapd", Kind: "requirement",
		TapdID: "1001", DeliveryState: "merged", DeliveryMRURL: "https://git.example.com/mr/7",
		ExternalRef:   &domain.ExternalRef{ExternalID: "2002", ExternalURL: "https://tapd.example.com/story/2002"},
		Clarification: domain.ClarificationAssessment{StartedAt: started},
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	views := s.IssueAnalyticsViews()
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	got := views[0]
	if got.ExternalRef == nil || got.ExternalRef.ExternalURL != "https://tapd.example.com/story/2002" || got.ExternalRef.ExternalID != "2002" {
		t.Fatalf("external ref = %+v", got.ExternalRef)
	}
	if got.TapdID != "1001" || got.DeliveryState != "merged" || got.DeliveryMRURL != "https://git.example.com/mr/7" {
		t.Fatalf("payload columns = %+v", got)
	}
	if !got.Clarification.StartedAt.Equal(started) {
		t.Fatalf("clarification startedAt = %v, want %v", got.Clarification.StartedAt, started)
	}
}

func TestBusinessDeliveryIssuesProjectsPayloadJSONColumns(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	started := time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)
	issue := domain.Issue{
		ID: "iss-lifecycle", ProjectID: "alpha", Title: "lifecycle", Status: "open", Kind: "requirement", Source: "platform",
		Clarification: domain.ClarificationAssessment{StartedAt: started},
		Operations:    []domain.IssueOperation{{At: started, Action: "create"}, {At: started.Add(time.Hour), Action: "close"}},
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	// CompletedAt is stamped by PutIssue on the transition to done.
	issue.Status = "done"
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.Issue(issue.ID)
	if !ok || stored.CompletedAt.IsZero() {
		t.Fatalf("stored completion = %+v (found=%v)", stored.CompletedAt, ok)
	}
	completed := stored.CompletedAt
	if err := s.PutIssue(domain.Issue{ID: "iss-other", ProjectID: "beta", Title: "other"}); err != nil {
		t.Fatal(err)
	}
	issues, err := s.AnalyticsRepo.businessDeliveryIssues([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].ID != "iss-lifecycle" {
		t.Fatalf("issues = %+v", issues)
	}
	got := issues[0]
	if !got.CompletedAt.Equal(completed) || !got.Clarification.StartedAt.Equal(started) {
		t.Fatalf("lifecycle times completed=%v started=%v", got.CompletedAt, got.Clarification.StartedAt)
	}
	if len(got.Operations) < 2 || got.Operations[0].Action != "create" || got.Operations[len(got.Operations)-1].Action != "close" {
		t.Fatalf("operations = %+v", got.Operations)
	}
}

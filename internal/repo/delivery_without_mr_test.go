package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A SOP run can succeed without delivering any MR: the work item may forbid
// pushing, or the run may only produce reports. "merged" promises the change is
// on the target branch, so the item must stay undelivered instead of claiming a
// merge that never happened.
func TestMarkIssueDeliveredFromSOPWithoutMRDoesNotClaimMerged(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	issue := domain.Issue{ID: "local-no-mr", ProjectID: "platform", Source: "local", Status: "in_review"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := s.MarkIssueDeliveredFromSOPMRs(issue.ID, nil, at); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Issue(issue.ID)
	if !ok {
		t.Fatal("issue disappeared after delivery signal")
	}
	if got.DeliveryState != "" {
		t.Fatalf("delivery without MR must not claim a merge, got DeliveryState=%q", got.DeliveryState)
	}
	if got.DeliveryLocked() {
		t.Fatalf("delivery without MR must not lock the item: %+v", got)
	}
	if got.Status != "done" {
		t.Fatalf("completed SOP should still finish the item, got Status=%q", got.Status)
	}
	if !got.DeliveryUpdatedAt.Equal(at) {
		t.Fatalf("DeliveryUpdatedAt=%s, want %s", got.DeliveryUpdatedAt, at)
	}
	if len(got.DeliveryMRList()) != 0 {
		t.Fatalf("delivery without MR must not invent MRs: %+v", got.DeliveryMRList())
	}
}

// A later run without an MR must not erase an earlier delivery: the recorded
// MR is still the evidence that the change landed.
func TestMarkIssueDeliveredFromSOPWithoutMRKeepsEarlierMRs(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	const url = "https://git.example.com/org/repo/merge_requests/41"
	issue := domain.Issue{ID: "local-earlier-mr", ProjectID: "platform", Source: "local", Status: "in_review"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	first := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	if err := s.MarkIssueDeliveredFromSOP(issue.ID, url, first); err != nil {
		t.Fatal(err)
	}
	second := first.Add(time.Hour)
	if err := s.MarkIssueDeliveredFromSOPMRs(issue.ID, nil, second); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if !got.DeliveryLocked() {
		t.Fatalf("earlier MR delivery must stay locked, got DeliveryState=%q", got.DeliveryState)
	}
	if !got.HasDeliveryMR(url) || len(got.DeliveryMRList()) != 1 {
		t.Fatalf("earlier delivery MR must survive a run without MR: %+v", got.DeliveryMRList())
	}
	if !got.DeliveryUpdatedAt.Equal(second) {
		t.Fatalf("DeliveryUpdatedAt=%s, want %s", got.DeliveryUpdatedAt, second)
	}
}

// The poll path can arrive without a URL (an MR row with no link yet). It must
// not turn the work item into a merged delivery either.
func TestMarkIssueDeliveredWithoutURLDoesNotClaimMerged(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	issue := domain.Issue{ID: "local-empty-url", ProjectID: "platform", Source: "local", Status: "in_review"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkIssueDelivered(issue.ID, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if got.DeliveryState != "" || got.DeliveryLocked() {
		t.Fatalf("poll without MR URL must not claim a merge, got DeliveryState=%q", got.DeliveryState)
	}
}

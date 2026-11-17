package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The time comparison only protects an already merged delivery; any other
// state (including a human reopen that refreshed DeliveryUpdatedAt) must not
// be skipped just because the signal is older than the last delivery update.
func TestIssueDeliverySkippedOnlyComparesTimeForMerged(t *testing.T) {
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	later := at.Add(time.Hour)
	cases := []struct {
		name    string
		issue   domain.Issue
		fromSOP bool
	}{
		{name: "no delivery state", issue: domain.Issue{Status: "in_review", DeliveryUpdatedAt: later}},
		{name: "opened delivery state", issue: domain.Issue{Status: "in_review", DeliveryState: "opened", DeliveryUpdatedAt: later}},
		{name: "reopened via SOP", issue: domain.Issue{Status: "todo", DeliveryState: "reopened", DeliveryUpdatedAt: later}, fromSOP: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if issueDeliverySkipped(tc.issue, nil, at, tc.fromSOP) {
				t.Fatalf("older signal must not skip a %q delivery", tc.issue.DeliveryState)
			}
		})
	}
}

func TestMarkIssueDeliveredZeroTimeUsesNow(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	past := time.Now().UTC().Add(-time.Hour)
	issue := domain.Issue{
		ID: "tapd-zero", ProjectID: "platform", Source: "tapd",
		Status: "done", DeliveryState: "merged", DeliveryUpdatedAt: past,
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Second)
	if err := s.MarkIssueDelivered(issue.ID, "https://git.example.com/org/repo/merge_requests/7", time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if got.DeliveryUpdatedAt.Before(before) {
		t.Fatalf("zero deliveredAt must default to now, got DeliveryUpdatedAt=%s (before %s)", got.DeliveryUpdatedAt, before)
	}
	if !got.DeliveryLocked() {
		t.Fatalf("issue should stay locked after delivery: %+v", got)
	}
}

func TestMarkIssueDeliveredPollKeepsOwnMRAgainstLeftover(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	const (
		ownURL      = "https://git.example.com/org/repo/merge_requests/286"
		leftoverURL = "https://git.example.com/org/repo/merge_requests/282"
	)
	issue := domain.Issue{ID: "tapd-own", ProjectID: "platform", Source: "tapd", Status: "in_review"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	sopAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := s.MarkIssueDeliveredFromSOP(issue.ID, ownURL, sopAt); err != nil {
		t.Fatal(err)
	}
	delivered, _ := s.Issue(issue.ID)
	if !delivered.HasDeliveryMR(ownURL) {
		t.Fatalf("SOP delivery should record its MR: %+v", delivered.DeliveryMRList())
	}

	// A poll reporting a leftover MR from an earlier run, with a newer
	// timestamp, must leave the work item untouched.
	if err := s.MarkIssueDelivered(issue.ID, leftoverURL, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Issue(issue.ID)
	if !got.DeliveryUpdatedAt.Equal(delivered.DeliveryUpdatedAt) {
		t.Fatalf("leftover MR poll touched delivery time: %s -> %s", delivered.DeliveryUpdatedAt, got.DeliveryUpdatedAt)
	}
	if got.HasDeliveryMR(leftoverURL) || len(got.DeliveryMRList()) != len(delivered.DeliveryMRList()) {
		t.Fatalf("leftover MR leaked into delivery MRs: %+v", got.DeliveryMRList())
	}
}

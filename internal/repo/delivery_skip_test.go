package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestIssueDeliverySkipped(t *testing.T) {
	const (
		ownURL   = "https://git.example.com/org/repo/merge_requests/286"
		otherURL = "https://git.example.com/org/repo/merge_requests/282"
	)
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	own := []domain.DeliveryMR{{URL: ownURL}}
	other := []domain.DeliveryMR{{URL: otherURL}}
	withOwn := domain.Issue{ID: "i-1", Status: "in_review", DeliveryMRs: own}

	cases := []struct {
		name    string
		issue   domain.Issue
		mrs     []domain.DeliveryMR
		at      time.Time
		fromSOP bool
		want    bool
	}{
		{name: "manually closed blocks poll", issue: domain.Issue{Status: "closed"}, mrs: own, at: at, want: true},
		{name: "manually closed blocks SOP", issue: domain.Issue{Status: "closed"}, mrs: own, at: at, fromSOP: true, want: true},
		{name: "externally closed is not manual", issue: domain.Issue{Status: "closed", CloseSource: "external"}, mrs: own, at: at, want: false},
		{name: "reopened blocks poll", issue: domain.Issue{DeliveryState: "Reopened"}, mrs: own, at: at, want: true},
		{name: "reopened allows SOP", issue: domain.Issue{DeliveryState: "reopened"}, mrs: own, at: at, fromSOP: true, want: false},
		{name: "leftover MR blocks poll", issue: withOwn, mrs: other, at: at, want: true},
		{name: "leftover MR allowed for SOP", issue: withOwn, mrs: other, at: at, fromSOP: true, want: false},
		{name: "own MR allows poll", issue: withOwn, mrs: own, at: at, want: false},
		{name: "no recorded MRs allows poll", issue: domain.Issue{}, mrs: other, at: at, want: false},
		{name: "merged with older signal", issue: domain.Issue{DeliveryState: "merged", DeliveryUpdatedAt: at}, mrs: nil, at: at.Add(-time.Minute), want: true},
		{name: "merged with same time", issue: domain.Issue{DeliveryState: "merged", DeliveryUpdatedAt: at}, mrs: nil, at: at, want: true},
		{name: "merged with newer signal", issue: domain.Issue{DeliveryState: "merged", DeliveryUpdatedAt: at}, mrs: nil, at: at.Add(time.Minute), want: false},
		{name: "merged older blocks SOP too", issue: domain.Issue{DeliveryState: "merged", DeliveryUpdatedAt: at}, mrs: nil, at: at.Add(-time.Minute), fromSOP: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := issueDeliverySkipped(tc.issue, tc.mrs, tc.at, tc.fromSOP); got != tc.want {
				t.Fatalf("issueDeliverySkipped() = %v, want %v", got, tc.want)
			}
		})
	}
}

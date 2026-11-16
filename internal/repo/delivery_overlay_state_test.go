package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestOverlayIssueMRState(t *testing.T) {
	const url = "https://git.example.com/org/repo/-/merge_requests/286"
	own := []domain.DeliveryMR{
		{URL: "https://git.example.com/org/repo/merge_requests/282", State: "closed"},
		{URL: "https://git.example.com/ORG/repo/merge_requests/286", State: "merged"},
	}
	cases := []struct {
		name  string
		state string
		url   string
		own   []domain.DeliveryMR
		want  string
	}{
		{name: "no own MRs", state: "opened", url: url, own: nil, want: "opened"},
		{name: "no matching MR", state: "unknown", url: "https://git.example.com/org/repo/merge_requests/999", own: own, want: "unknown"},
		{name: "unknown falls back to recorded", state: "unknown", url: url, own: own, want: "merged"},
		{name: "empty falls back to recorded", state: "", url: url, own: own, want: "merged"},
		{name: "empty recorded keeps unknown", state: "unknown", url: url, own: []domain.DeliveryMR{{URL: url}}, want: "unknown"},
		{name: "integrated upgraded by merged", state: "integrated", url: url, own: own, want: "merged"},
		{name: "opened not overridden", state: "opened", url: url, own: own, want: "opened"},
		{name: "first match wins", state: "unknown", url: url, own: []domain.DeliveryMR{{URL: url, State: "opened"}, {URL: url, State: "merged"}}, want: "opened"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := overlayIssueMRState(tc.state, tc.url, tc.own); got != tc.want {
				t.Fatalf("overlayIssueMRState(%q) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}

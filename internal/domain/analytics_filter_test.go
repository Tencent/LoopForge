package domain

import "testing"

func TestAnalyticsFilterMatchesProject(t *testing.T) {
	cases := []struct {
		name   string
		filter AnalyticsFilter
		id     string
		want   bool
	}{
		{name: "unrestricted", filter: AnalyticsFilter{}, id: "p1", want: true},
		{name: "empty scope means no projects", filter: AnalyticsFilter{ProjectIDs: []string{}}, id: "p1", want: false},
		{name: "in scope", filter: AnalyticsFilter{ProjectIDs: []string{"p0", "p1"}}, id: "p1", want: true},
		{name: "out of scope", filter: AnalyticsFilter{ProjectIDs: []string{"p0"}}, id: "p1", want: false},
		{name: "exact match only", filter: AnalyticsFilter{ProjectIDs: []string{"P1"}}, id: "p1", want: false},
		{name: "project id narrows", filter: AnalyticsFilter{ProjectID: "p0"}, id: "p1", want: false},
		{name: "project id matches", filter: AnalyticsFilter{ProjectID: "p1"}, id: "p1", want: true},
		{name: "project id still needs scope", filter: AnalyticsFilter{ProjectID: "p1", ProjectIDs: []string{"p0"}}, id: "p1", want: false},
		{name: "project id and scope", filter: AnalyticsFilter{ProjectID: "p1", ProjectIDs: []string{"p1"}}, id: "p1", want: true},
	}
	for _, tc := range cases {
		if got := tc.filter.MatchesProject(tc.id); got != tc.want {
			t.Errorf("%s: MatchesProject(%q) = %v, want %v", tc.name, tc.id, got, tc.want)
		}
	}
}

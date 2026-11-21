package domain

import "testing"

func TestAnalyticsFilterMatchesProject(t *testing.T) {
	cases := []struct {
		name   string
		filter AnalyticsFilter
		id     string
		want   bool
	}{
		{name: "unassigned includes empty", filter: AnalyticsFilter{ProjectScope: "unassigned"}, id: "", want: true},
		{name: "unassigned excludes project", filter: AnalyticsFilter{ProjectScope: "unassigned"}, id: "p1", want: false},
		{name: "unassigned preserves denied scope", filter: AnalyticsFilter{ProjectScope: "unassigned", ProjectIDs: []string{}}, id: "", want: false},
		{name: "unassigned intersects allowed projects", filter: AnalyticsFilter{ProjectScope: "unassigned", ProjectIDs: []string{"p1"}}, id: "", want: false},
		{name: "unassigned allowed explicitly", filter: AnalyticsFilter{ProjectScope: "unassigned", ProjectIDs: []string{""}}, id: "", want: true},
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

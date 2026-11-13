package httpapi

import "testing"

func TestGitHubChecksOverall(t *testing.T) {
	check := func(state string) githubCheckSummary { return githubCheckSummary{Name: state, State: state} }
	cases := []struct {
		name     string
		checks   []githubCheckSummary
		warnings []string
		want     string
	}{
		{name: "no checks", want: "none"},
		{name: "all success", checks: []githubCheckSummary{check("success"), check("success")}, want: "success"},
		{name: "pending wins over success", checks: []githubCheckSummary{check("success"), check("in_progress")}, want: "pending"},
		{name: "failure wins over pending", checks: []githubCheckSummary{check("failure"), check("queued")}, want: "failure"},
		{name: "failure not downgraded by later pending", checks: []githubCheckSummary{check("queued"), check("failure"), check("pending")}, want: "failure"},
		{name: "warning with no checks is unknown", warnings: []string{"w"}, want: "unknown"},
		{name: "warning downgrades success to unknown", checks: []githubCheckSummary{check("success")}, warnings: []string{"w"}, want: "unknown"},
		{name: "warning keeps pending", checks: []githubCheckSummary{check("queued")}, warnings: []string{"w"}, want: "pending"},
		{name: "warning keeps failure", checks: []githubCheckSummary{check("failure")}, warnings: []string{"w"}, want: "failure"},
	}
	for _, tc := range cases {
		if got := githubChecksOverall(tc.checks, tc.warnings); got != tc.want {
			t.Errorf("%s: githubChecksOverall() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

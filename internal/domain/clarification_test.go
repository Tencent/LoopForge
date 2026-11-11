package domain

import "testing"

func TestShouldResumeClarificationOnlyForCrashedAnalysis(t *testing.T) {
	cases := []struct {
		name  string
		issue Issue
		want  bool
	}{
		{"analyzing", Issue{ID: "a", Clarification: ClarificationAssessment{Status: ClarificationAnalyzing}}, true},
		{"fresh import", Issue{ID: "a", Kind: "requirement"}, false},
		{"legacy needs_user", Issue{ID: "a", Kind: "requirement", Clarification: ClarificationAssessment{Status: ClarificationNeedsUser, Gated: true}}, false},
		{"waiting for answers", Issue{ID: "a", Clarification: ClarificationAssessment{Status: ClarificationInProgress}}, false},
		{"no id", Issue{Clarification: ClarificationAssessment{Status: ClarificationAnalyzing}}, false},
	}
	for _, tc := range cases {
		if got := ShouldResumeClarification(tc.issue); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

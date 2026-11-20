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

func TestClarifyRequiredBeforeStart(t *testing.T) {
	required, skip := Project{ClarifyBeforeStart: ClarifyBeforeStartRequired}, Project{}
	follow, needs, skips := WorkflowDefinition{ID: "a"}, WorkflowDefinition{ID: "b", ClarifyBeforeStart: ClarifyBeforeStartRequired}, WorkflowDefinition{ID: "c", ClarifyBeforeStart: ClarifyBeforeStartSkip}
	for _, tc := range []struct {
		name      string
		project   Project
		workflows []WorkflowDefinition
		want      bool
	}{
		{"project asks, SOP follows", required, []WorkflowDefinition{follow}, true},
		{"project asks, SOP skips", required, []WorkflowDefinition{skips}, false},
		{"project does not ask, SOP asks", skip, []WorkflowDefinition{needs}, true},
		{"project does not ask, SOP follows", skip, []WorkflowDefinition{follow}, false},
		{"one SOP of the chain asks", skip, []WorkflowDefinition{follow, needs}, true},
		{"no SOP follows the project", required, nil, true},
		{"nothing configured", skip, nil, false},
	} {
		if got := ClarifyRequiredBeforeStart(tc.project, tc.workflows); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
	for _, v := range []string{"", ClarifyBeforeStartRequired, ClarifyBeforeStartSkip} {
		if err := ValidateClarifyBeforeStart(v); err != nil {
			t.Errorf("%q rejected: %v", v, err)
		}
	}
	if ValidateClarifyBeforeStart("always") == nil {
		t.Error("unknown value accepted")
	}
}

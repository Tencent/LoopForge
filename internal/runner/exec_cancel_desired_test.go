package runner

import "testing"

func TestRunnerTaskCancelDesired(t *testing.T) {
	cases := map[string]bool{
		"cancel":  true,
		"CANCEL":  true,
		"Cancel":  true,
		"":        false,
		"run":     false,
		"pause":   false,
		" cancel": false,
	}
	for state, want := range cases {
		if got := runnerTaskCancelDesired(state); got != want {
			t.Errorf("runnerTaskCancelDesired(%q) = %v, want %v", state, got, want)
		}
	}
}

package domain

import "testing"

func TestRunnerDefaultSelectionBlocker(t *testing.T) {
	for _, tc := range []struct {
		status, version string
		ok              bool
	}{
		{RunnerStatusOnline, "0.8.0", true},
		{RunnerStatusOnline, "v0.9.2", true},
		{RunnerStatusOnline, "0.7.9", false},
		{RunnerStatusOnline, "dev", false},
		{RunnerStatusOffline, "0.9.2", false},
	} {
		got := Runner{Status: tc.status, RunnerVersion: tc.version}.DefaultSelectionBlocker()
		if (got == "") != tc.ok {
			t.Errorf("%s/%s: blocker %q, want ok=%v", tc.status, tc.version, got, tc.ok)
		}
	}
}

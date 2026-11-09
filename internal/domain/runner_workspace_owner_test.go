package domain

import "testing"

func TestWorkspaceOwnerPreparationRequiresNewRunnerVersion(t *testing.T) {
	for _, tt := range []struct {
		version string
		want    bool
	}{
		{"0.6.0", false}, {"0.6.1", false}, {"0.7.0", false}, {"0.7.1", true}, {"0.8.0", true},
	} {
		t.Run(tt.version, func(t *testing.T) {
			runner := Runner{RunnerVersion: tt.version}
			if got := runner.SupportsWorkspaceOwnerPreparation(); got != tt.want {
				t.Fatalf("SupportsWorkspaceOwnerPreparation() = %v, want %v", got, tt.want)
			}
		})
	}
}

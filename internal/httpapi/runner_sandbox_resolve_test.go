package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

func TestResolveRunnerTaskSandboxByRunnerOS(t *testing.T) {
	stored := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "box", Driver: "native"}}
	task := domain.RunnerTask{ID: "t", Sandbox: stored}
	for goos, want := range map[string]string{"linux": "bwrap", "darwin": "seatbelt", "": "native", "windows": "native"} {
		got := resolveRunnerTaskSandbox(task, goos)
		if got.Sandbox.Profile.Driver != want {
			t.Fatalf("%q runner got driver %q, want %q", goos, got.Sandbox.Profile.Driver, want)
		}
	}
	if stored.Profile.Driver != "native" {
		t.Fatal("resolution must not mutate the stored task sandbox")
	}
	docker := domain.RunnerTask{Sandbox: &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{Driver: "docker"}}}
	if resolveRunnerTaskSandbox(docker, "darwin").Sandbox.Profile.Driver != "docker" {
		t.Fatal("non-native drivers must pass through")
	}
	if resolveRunnerTaskSandbox(domain.RunnerTask{}, "linux").Sandbox != nil {
		t.Fatal("tasks without a sandbox stay without one")
	}
}

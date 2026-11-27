package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestDockerNetworkIsolatedRunArgs(t *testing.T) {
	args := dockerNetworkIsolatedRunArgs(
		"/tmp/ws",
		"loopforge:local",
		domain.SandboxProfile{Driver: domain.SandboxDriverDocker, ReadOnlyRootFS: true},
		dockerSandboxLimits{},
		"1000:1000",
		CommandSpec{
			Argv:            []string{"sh", "-lc", "cat triage.json"},
			NetworkIsolated: true,
			environment:     map[string]string{"AGENTFLOW_WORKSPACE": "/workspace"},
		},
	)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm --init",
		"--network none",
		"--cap-drop=ALL",
		"--security-opt no-new-privileges",
		"-v /tmp/ws:/workspace",
		"--user 1000:1000",
		"--read-only",
		"--entrypoint sh loopforge:local",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("dockerNetworkIsolatedRunArgs missing %q in %q", want, joined)
		}
	}
	if !slices.Contains(args, "AGENTFLOW_WORKSPACE=/workspace") {
		t.Fatalf("expected workspace env in args: %v", args)
	}
}

func TestNonDockerSandboxesRejectNetworkIsolatedExec(t *testing.T) {
	local := &localSandbox{id: "l1", workspace: t.TempDir()}
	if _, err := local.Exec(context.Background(), CommandSpec{Argv: []string{"true"}, NetworkIsolated: true}, nil); err == nil || !strings.Contains(err.Error(), "非特权 Docker") {
		t.Fatalf("expected localSandbox to reject NetworkIsolated exec, got %v", err)
	}
	proc := &processSandbox{id: "p1", workspace: t.TempDir(), driver: "bwrap", profile: domain.SandboxProfile{Driver: "bwrap"}}
	if _, err := proc.Exec(context.Background(), CommandSpec{Argv: []string{"true"}, NetworkIsolated: true}, nil); err == nil || !strings.Contains(err.Error(), "非特权 Docker") {
		t.Fatalf("expected processSandbox to reject NetworkIsolated exec, got %v", err)
	}
	priv := &dockerSandbox{id: "d1", workspace: t.TempDir(), profile: domain.SandboxProfile{Driver: domain.SandboxDriverDocker, Privileged: true}}
	if _, err := priv.Exec(context.Background(), CommandSpec{Argv: []string{"true"}, NetworkIsolated: true}, nil); err == nil || !strings.Contains(err.Error(), "特权 Docker") {
		t.Fatalf("expected privileged dockerSandbox to reject NetworkIsolated exec, got %v", err)
	}
}

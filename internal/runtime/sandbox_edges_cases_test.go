package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

type cvRManagedInner struct {
	workspace string
	recreated int
	bound     int
}

func (s *cvRManagedInner) ID() string                  { return "inner" }
func (s *cvRManagedInner) Workspace() string           { return s.workspace }
func (s *cvRManagedInner) Close(context.Context) error { return nil }
func (s *cvRManagedInner) Exec(context.Context, CommandSpec, EventSink) (string, error) {
	return "ok", nil
}
func (s *cvRManagedInner) Recreate(context.Context) error      { s.recreated++; return nil }
func (s *cvRManagedInner) BindWorkspace(context.Context) error { s.bound++; return nil }
func (s *cvRManagedInner) IsolationEnv() map[string]string     { return map[string]string{"INNER": "1"} }

func TestProcessSandboxExecAndClose(t *testing.T) {
	workspace := t.TempDir()
	bin := filepath.Join(t.TempDir(), "seatbelt")
	script := "#!/bin/sh\nshift 2\nexec \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sb := &processSandbox{id: "p", workspace: workspace, driver: "seatbelt", binary: bin, profile: domain.SandboxProfile{Environment: map[string]string{"CVR": "yes"}}}
	out, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{"sh", "-c", "printf \"$CVR\""}}, nil)
	if err != nil || strings.TrimSpace(out) != "yes" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSandboxForwardersAndDetach(t *testing.T) {
	inner := &cvRManagedInner{workspace: t.TempDir()}
	lock, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	sb := &managedSandbox{Sandbox: inner, slots: make(chan struct{}, 1), lock: lock, workspace: inner.workspace}
	if sb.Driver() != "" || sb.IsolationEnv()["INNER"] != "1" {
		t.Fatal("forwarded metadata")
	}
	if err := sb.Recreate(context.Background()); err != nil || inner.recreated != 1 {
		t.Fatal("recreate")
	}
	if err := sb.BindWorkspace(context.Background()); err != nil || inner.bound != 1 {
		t.Fatal("bind")
	}
	if err := sb.Detach(); err != nil {
		t.Fatal(err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	plain := &managedSandbox{Sandbox: &localSandbox{id: "l", workspace: t.TempDir()}, slots: make(chan struct{}, 1)}
	if err := plain.Recreate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := plain.BindWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAgentStateMountsAndDockerManagerReclaim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".cvR")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, err := dockerAgentStateMountRequirements(SandboxRequirements{CLIStates: []CLIStateRequirement{{Directory: ".cvR", Files: []string{"auth.json", "missing"}}}})
	if err != nil || len(args) != 4 {
		t.Fatalf("args=%v err=%v", args, err)
	}
	if err := os.WriteFile(filepath.Join(home, "not-dir"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dockerAgentStateMountRequirements(SandboxRequirements{CLIStates: []CLIStateRequirement{{Directory: "not-dir"}}}); err == nil {
		t.Fatal("state file accepted as directory")
	}

	workspace := filepath.Join(t.TempDir(), "run-one")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	cvRInstallFakeDocker(t, workspace)
	dockerIdentity, err := ReadSandboxEnvironmentIdentity(context.Background(), &dockerSandbox{name: "box", workspace: workspace})
	if err != nil || !strings.HasPrefix(dockerIdentity, "docker:") {
		t.Fatalf("docker identity=%q err=%v", dockerIdentity, err)
	}
	m := NewManager(filepath.Dir(workspace))
	idle, err := m.RunsWithIdleDockerSandbox(context.Background(), []string{filepath.Base(workspace), "absent"})
	if err != nil || len(idle) != 1 {
		t.Fatalf("idle=%v err=%v", idle, err)
	}
	removed, err := m.ReclaimDockerSandboxes(context.Background(), nil)
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	m.DiscardRunContainer(context.Background(), filepath.Base(workspace))
}

func TestSandboxTerminalCommandsAcrossDrivers(t *testing.T) {
	workspace := t.TempDir()
	local := &localSandbox{id: "local", workspace: workspace, environment: map[string]string{"A": "1"}}
	cmd, home, err := sandboxTerminalCommand(context.Background(), local)
	if err != nil || cmd.Dir != workspace || home == "" {
		t.Fatalf("local cmd=%+v home=%q err=%v", cmd, home, err)
	}
	_ = removeTerminalHome(home)
	if _, _, err := sandboxTerminalCommand(context.Background(), nil); err == nil {
		t.Fatal("nil sandbox accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := sandboxTerminalCommand(ctx, local); err == nil {
		t.Fatal("canceled terminal accepted")
	}
	bin := filepath.Join(t.TempDir(), "bwrap")
	_ = os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	process := &processSandbox{id: "process", workspace: workspace, driver: "bwrap", binary: bin}
	if cmd, home, err = sandboxTerminalCommand(context.Background(), process); err != nil || cmd == nil || home == "" {
		t.Fatalf("process cmd=%+v home=%q err=%v", cmd, home, err)
	}
	_ = removeTerminalHome(home)
	cvRInstallFakeDocker(t, workspace)
	docker := &dockerSandbox{id: "docker", name: "box", workspace: workspace, started: true}
	if cmd, home, err = sandboxTerminalCommand(context.Background(), docker); err != nil || cmd == nil || home == "" {
		t.Fatalf("docker cmd=%+v home=%q err=%v", cmd, home, err)
	}
	sandboxTerminalCleanup(docker, home)
	_ = removeTerminalHome(home)
	managed := &managedSandbox{Sandbox: local, workspace: workspace, credentials: RunCredentials{GitToken: "token"}, slots: make(chan struct{}, 1)}
	if cmd, home, err = sandboxTerminalCommand(context.Background(), managed); err != nil || cmd == nil || home == "" {
		t.Fatalf("managed cmd=%+v home=%q err=%v", cmd, home, err)
	}
	_ = removeTerminalHome(home)
}

func TestWorkspacePreparationReusableSimple(t *testing.T) {
	workflow := domain.WorkflowDefinition{}
	state := WorkspacePreparationStatus{Status: "ready", Environment: "epoch"}
	if !WorkspacePreparationReusable(state, workflow, nil, "epoch") {
		t.Fatal("empty ready preparation not reusable")
	}
	state.Status = "failed"
	if WorkspacePreparationReusable(state, workflow, nil, "epoch") {
		t.Fatal("failed preparation reusable")
	}
	state.Status = "ready"
	if WorkspacePreparationReusable(state, workflow, nil, "other") {
		t.Fatal("wrong environment reusable")
	}
}

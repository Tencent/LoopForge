package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWorkspaceTerminalSandboxUsesOverlayAndPrivateCredentials(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("AGENTFLOW_DATABASE_URL", "server-secret")
	t.Setenv("AGENTFLOW_TEST_SECRET", "another-secret")
	t.Setenv("OPENAI_API_KEY", "host-model-secret")
	bin := filepath.Join(workspace, "tools")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "env-tool"), []byte("#!/bin/sh\nprintf '%s' \"$SOP_ENV_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sb := &managedSandbox{Sandbox: &localSandbox{workspace: workspace, environment: map[string]string{"PATH": bin + ":" + os.Getenv("PATH"), "SOP_ENV_MARKER": "same-environment"}}, workspace: workspace, credentials: RunCredentials{GitToken: "run-owned-token", CodeBuddy: CodeBuddyCredential{APIKey: "not-for-terminal"}}}
	var released atomic.Int32
	var pool TerminalPool
	ctx, cancel := context.WithCancel(context.Background())
	opened, err := pool.HandleSandbox(ctx, sb, "run:1", domain.WorkspaceTerminalRequest{Operation: "open"}, func() { released.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer pool.CloseScope("run:1")
	cancel() // HTTP lifetime must not kill a terminal returned to the browser.
	input := "env-tool > tool-result; printf '%s|%s|%s|%s' \"$AGENTFLOW_DATABASE_URL\" \"$AGENTFLOW_TEST_SECRET\" \"$OPENAI_API_KEY\" \"$CODEBUDDY_API_KEY\" > leaked; printf '%s' \"$AGENTFLOW_GIT_TOKEN\" > git-token\n"
	if _, err := pool.HandleSandbox(context.Background(), nil, "run:1", domain.WorkspaceTerminalRequest{Operation: "input", ID: opened.ID, Data: input}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, _ := os.ReadFile(filepath.Join(workspace, "tool-result"))
		token, _ := os.ReadFile(filepath.Join(workspace, "git-token"))
		if string(body) == "same-environment" && string(token) == "run-owned-token" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if body, _ := os.ReadFile(filepath.Join(workspace, "tool-result")); string(body) != "same-environment" {
		t.Fatalf("sandbox PATH/overlay not active: %q", body)
	}
	if body, _ := os.ReadFile(filepath.Join(workspace, "leaked")); string(body) != "|||" {
		t.Fatalf("host/model environment leaked: %q", body)
	}
	if body, _ := os.ReadFile(filepath.Join(workspace, "git-token")); string(body) != "run-owned-token" {
		t.Fatalf("explicit run Git binding missing: %q", body)
	}
	pool.CloseScope("run:1")
	pool.CloseScope("run:1")
	if got := released.Load(); got != 1 {
		t.Fatalf("release called %d times", got)
	}
}

type unsupportedTerminalSandbox struct{ workspace string }

func (s unsupportedTerminalSandbox) ID() string        { return "unsupported" }
func (s unsupportedTerminalSandbox) Workspace() string { return s.workspace }
func (s unsupportedTerminalSandbox) Exec(context.Context, CommandSpec, EventSink) (string, error) {
	panic("terminal must not fall back to Exec or host")
}
func (s unsupportedTerminalSandbox) Close(context.Context) error { return nil }

func TestWorkspaceTerminalRejectsUnknownDriverWithoutReleasingCaller(t *testing.T) {
	var pool TerminalPool
	var released atomic.Int32
	_, err := pool.HandleSandbox(context.Background(), unsupportedTerminalSandbox{t.TempDir()}, "run:1", domain.WorkspaceTerminalRequest{Operation: "open"}, func() { released.Add(1) })
	if err == nil || !strings.Contains(err.Error(), "不能降级") {
		t.Fatalf("unexpected error %v", err)
	}
	if released.Load() != 0 || pool.HasScope("run:1") {
		t.Fatal("failed open transferred callback/session ownership")
	}
}

func TestWorkspaceTerminalDockerCommandStaysInSameContainer(t *testing.T) {
	t.Setenv("AGENTFLOW_DATABASE_URL", "private")
	t.Setenv("OPENAI_API_KEY", "private")
	s := &dockerSandbox{name: "existing-run-container", profile: domain.SandboxProfile{Environment: map[string]string{"SOP_ENV_MARKER": "container-env"}}}
	cmd := s.terminalCommand(CommandSpec{Argv: []string{"/bin/sh", "-i"}, environment: map[string]string{"HOME": "/workspace/.agentflow/private-home/terminal-test"}})
	args := strings.Join(cmd.Args, "\n")
	for _, want := range []string{"exec\n-it", "existing-run-container", "cd /workspace", "/bin/sh\n-i", "SOP_ENV_MARKER=container-env", "HOME=/workspace/.agentflow/private-home/terminal-test"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q from %q", want, args)
		}
	}
	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "AGENTFLOW_DATABASE_URL=") || strings.HasPrefix(env, "OPENAI_API_KEY=") {
			t.Fatal("Docker host process inherited server secret")
		}
	}
}

func TestWorkspaceEnvironmentIdentityStableAcrossHandlesAndChangesWithEnvironment(t *testing.T) {
	workspace := t.TempDir()
	a := &localSandbox{workspace: workspace, environment: map[string]string{"FLAG": "one"}}
	first, err := SandboxEnvironmentIdentity(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SandboxEnvironmentIdentity(context.Background(), &localSandbox{workspace: workspace, environment: map[string]string{"FLAG": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("same environment handle changed epoch")
	}
	third, err := SandboxEnvironmentIdentity(context.Background(), &localSandbox{workspace: workspace, environment: map[string]string{"FLAG": "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("different environment reused preparation epoch")
	}
	if strings.Contains(third, "two") {
		t.Fatal("raw environment leaked into identity")
	}
}

func TestTerminalHomeCleanupDoesNotFollowWorkspaceSymlink(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	home, err := EnsureControlDirectory(workspace, "private-home", "terminal-test")
	if err != nil {
		t.Fatal(err)
	}
	outsideHome := filepath.Join(outside, "terminal-test")
	if err := os.Mkdir(outsideHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideHome, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(home)
	if err := os.Rename(parent, parent+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	_ = removeTerminalHome(home)
	if data, err := os.ReadFile(filepath.Join(outsideHome, "keep")); err != nil || string(data) != "keep" {
		t.Fatal("terminal cleanup followed a workspace symlink")
	}
}

func TestWorkspaceTerminalSandboxFailedPTYKeepsCallerRelease(t *testing.T) {
	var pool TerminalPool
	var released atomic.Int32
	sandbox := &processSandbox{workspace: t.TempDir(), driver: "seatbelt", binary: filepath.Join(t.TempDir(), "missing-sandbox-launcher")}
	_, err := pool.HandleSandbox(context.Background(), sandbox, "run:1", domain.WorkspaceTerminalRequest{Operation: "open"}, func() { released.Add(1) })
	if err == nil {
		t.Fatal("missing sandbox launcher unexpectedly opened")
	}
	if released.Load() != 0 || pool.HasScope("run:1") {
		t.Fatal("failed PTY took ownership of caller release")
	}
}

// Opt-in integration uses an already-present image and a disposable container.
// It never pulls an image or reuses a run's container.
func TestWorkspaceTerminalDockerRealPTY(t *testing.T) {
	if os.Getenv("AGENTFLOW_TEST_TERMINAL_DOCKER") != "1" {
		t.Skip("set AGENTFLOW_TEST_TERMINAL_DOCKER=1 for disposable local Docker PTY validation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Colima typically shares /Users but not macOS's /var/folders temp root.
	workspace, workspaceErr := os.MkdirTemp(".", ".terminal-docker-test-")
	if workspaceErr != nil {
		t.Fatal(workspaceErr)
	}
	workspace, workspaceErr = filepath.Abs(workspace)
	if workspaceErr != nil {
		t.Fatal(workspaceErr)
	}
	defer os.RemoveAll(workspace)
	defer os.RemoveAll(dockerIsolationMaskRoot(workspace))
	name := fmt.Sprintf("agentflow-terminal-qa-%d", time.Now().UnixNano())
	create := exec.CommandContext(ctx, "docker", "run", "--pull=never", "--rm", "-d", "--name", name, "-v", workspace+":/workspace", "alpine:3.20", "sh", "-c", "sleep 60")
	if output, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create isolated container: %s %v", output, err)
	}
	defer exec.Command("docker", "rm", "-f", name).Run()
	defer exec.Command("docker", "rm", "-f", dockerScriptContainerName(name)).Run()
	sb := &dockerSandbox{id: name, name: name, workspace: workspace, started: true}
	if output, err := sb.Exec(ctx, CommandSpec{Argv: []string{"sh", "-c", "printf sandbox-only > /tmp/terminal-env-marker"}}, nil); err != nil {
		t.Fatalf("SOP command: %s %v", output, err)
	}
	before, err := SandboxEnvironmentIdentity(ctx, sb)
	if err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "environment", Name: "准备环境", Type: "script", RunOnWorkspaceRebuild: true, Script: `mkdir -p .agentflow/prep
printf 'run\n' >> .agentflow/prep/environment-count
printf ready > /tmp/preparation-marker`}}}
	for i := 0; i < 2; i++ {
		if state, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil); err != nil || state.Status != "ready" {
			t.Fatalf("initial prep: %+v %v", state, err)
		}
	}
	if preparationCount(t, sb, "environment") != 1 {
		t.Fatal("warm Docker environment repeated preparation")
	}
	if err := os.WriteFile(filepath.Join(workspace, "user-change"), []byte("keep my dirty edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	var pool TerminalPool
	opened, err := pool.HandleSandbox(ctx, sb, "docker:1", domain.WorkspaceTerminalRequest{Operation: "open"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.CloseScope("docker:1")
	terminal := terminalPTYTest{t: t, pool: &pool, scope: "docker:1", id: opened.ID}
	terminal.waitFor("root@" + name + " /workspace # ")
	input := "cat /tmp/terminal-env-marker > terminal-result; printf '%s' \"$HOME\" > terminal-home; echo $$ > terminal-pid\n"
	if _, err := pool.HandleSandbox(ctx, nil, "docker:1", domain.WorkspaceTerminalRequest{Operation: "input", ID: opened.ID, Data: input}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(workspace, "terminal-result"))
		home, _ := os.ReadFile(filepath.Join(workspace, "terminal-home"))
		pid, _ := os.ReadFile(filepath.Join(workspace, "terminal-pid"))
		if string(data) == "sandbox-only" && len(home) > 0 && len(pid) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if data, _ := os.ReadFile(filepath.Join(workspace, "terminal-result")); string(data) != "sandbox-only" {
		result, readErr := pool.HandleSandbox(ctx, nil, "docker:1", domain.WorkspaceTerminalRequest{Operation: "read", ID: opened.ID}, nil)
		inside, _ := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", "pwd; ls -la /workspace; cat /workspace/terminal-result; cat /workspace/terminal-home; ls /tmp/terminal-env-marker").CombinedOutput()
		t.Fatalf("terminal did not share SOP container runtime state: %q PTY=%q exited=%v read=%v inside=%q hostdir=%q", data, result.Data, result.Exited, readErr, inside, workspace)
	}
	if data, _ := os.ReadFile(filepath.Join(workspace, "terminal-home")); !strings.HasPrefix(string(data), "/workspace/.agentflow/private-home/terminal-") {
		t.Fatalf("wrong container HOME: %q", data)
	}
	pool.CloseScope("docker:1")
	deadline = time.Now().Add(3 * time.Second)
	alive := true
	for time.Now().Before(deadline) {
		err := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", `kill -0 "$(cat /workspace/terminal-pid)" 2>/dev/null`).Run()
		if err != nil {
			alive = false
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if alive {
		t.Fatal("closing Docker PTY leaked its container shell")
	}
	// Closing before the Docker client finishes attaching must not leave a shell.
	immediate, err := pool.HandleSandbox(ctx, sb, "docker:instant", domain.WorkspaceTerminalRequest{Operation: "open"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.mu.Lock()
	immediateToken := filepath.Base(pool.sessions[immediate.ID].home)
	pool.mu.Unlock()
	pool.CloseScope("docker:instant")
	// Match the session token in process environments rather than its shell
	// executable: both the Bash launcher and /bin/sh fallback must be reaped.
	processes, err := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", `for environ in /proc/[0-9]*/environ; do
  [ -r "$environ" ] || continue
  if tr '\000' '\n' 2>/dev/null < "$environ" | grep -Fxq "AGENTFLOW_TERMINAL_ID=$1"; then
    printf '%s\n' "$environ"
  fi
done`, "terminal-processes", immediateToken).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect disposable processes: %s %v", processes, err)
	}
	if len(strings.TrimSpace(string(processes))) > 0 {
		t.Fatalf("immediately closed Docker PTY leaked shell: %s", processes)
	}
	if output, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
		t.Fatalf("remove disposable environment: %s %v", output, err)
	}
	if _, err := ReadSandboxEnvironmentIdentity(ctx, sb); err == nil {
		t.Fatal("missing environment reported a warm identity")
	}
	if output, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "name=^/"+name+"$").Output(); err != nil || len(strings.TrimSpace(string(output))) > 0 {
		t.Fatalf("read-only identity recreated container: %q %v", output, err)
	}
	if output, err := exec.CommandContext(ctx, create.Args[0], create.Args[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("recreate disposable environment: %s %v", output, err)
	}
	sb.started = false
	after, err := SandboxEnvironmentIdentity(ctx, sb)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("recreated container reused stale preparation identity")
	}
	if _, err := sb.Exec(ctx, CommandSpec{Argv: []string{"test", "-f", "/tmp/preparation-marker"}}, nil); err == nil {
		t.Fatal("recreated container unexpectedly retained writable-layer marker")
	}
	if state, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil); err != nil || state.Status != "ready" {
		t.Fatalf("recreated prep: %+v %v", state, err)
	}
	if preparationCount(t, sb, "environment") != 2 {
		t.Fatal("persisted file stamp suppressed preparation in recreated container")
	}
	if _, err := sb.Exec(ctx, CommandSpec{Argv: []string{"test", "-f", "/tmp/preparation-marker"}}, nil); err != nil {
		t.Fatal("recreated environment did not reinstall container-local state")
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "user-change")); err != nil || string(data) != "keep my dirty edit" {
		t.Fatal("environment replay changed restored user files")
	}
}

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWorkspaceTerminalInteractivePromptAndTabCompletion(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("Bash is required to validate readline completion")
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	bin := prepareTerminalCompletionFiles(t, workspace)
	sb := &localSandbox{id: "run-terminal-local", workspace: workspace, environment: map[string]string{
		"PATH": bin + ":" + os.Getenv("PATH"),
	}}
	suffix := "$ "
	if os.Geteuid() == 0 {
		suffix = "# "
	}
	exerciseTerminalPromptAndCompletion(t, sb, currentUser.Username, workspace, workspace, suffix)
}

// This image has dash as /bin/sh and Bash available separately. Checking the
// actual PTY catches regressions that argv assertions cannot: launching sh -i
// succeeds, but omits both Bash prompt expansion and readline completion.
func TestWorkspaceTerminalDockerBashPromptAndTabCompletion(t *testing.T) {
	if os.Getenv("AGENTFLOW_TEST_TERMINAL_DOCKER") != "1" {
		t.Skip("set AGENTFLOW_TEST_TERMINAL_DOCKER=1 for disposable local Docker PTY validation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Colima shares the checkout, whereas t.TempDir may be outside its mounts.
	workspace, err := os.MkdirTemp(".", ".terminal-bash-test-")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workspace)
	prepareTerminalCompletionFiles(t, workspace)
	name := fmt.Sprintf("run-terminal-bash-%d", time.Now().UnixNano())
	create := exec.CommandContext(ctx, "docker", "run", "--pull=never", "--rm", "-d", "--name", name, "-v", workspace+":/workspace", "node:22-bookworm-slim", "sh", "-c", "sleep 60")
	if output, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create disposable Bash container: %s %v", output, err)
	}
	defer exec.Command("docker", "rm", "-f", name).Run()
	if output, err := exec.CommandContext(ctx, "docker", "exec", name, "readlink", "/bin/sh").Output(); err != nil || strings.TrimSpace(string(output)) != "dash" {
		t.Fatalf("test requires Debian dash as /bin/sh: %q %v", output, err)
	}
	sb := &dockerSandbox{id: name, name: name, workspace: workspace, started: true, profile: domain.SandboxProfile{
		Environment: map[string]string{"PATH": "/workspace/.terminal-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
	}}
	exerciseTerminalPromptAndCompletion(t, sb, "root", "/workspace", workspace, "# ")
}

func prepareTerminalCompletionFiles(t *testing.T, workspace string) string {
	t.Helper()
	child := filepath.Join(workspace, "prompt-child")
	bin := filepath.Join(workspace, ".terminal-bin")
	for _, path := range []string{child, bin} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(child, "agentflow-tab-file.txt"), []byte("file-completion-ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "agentflow-terminal-completion-unique"), []byte("#!/bin/sh\nprintf command-completion-ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func exerciseTerminalPromptAndCompletion(t *testing.T, sb Sandbox, username, terminalWorkspace, hostWorkspace, suffix string) {
	t.Helper()
	var pool TerminalPool
	scope := "interactive:" + sb.ID()
	opened, err := pool.HandleSandbox(context.Background(), sb, scope, domain.WorkspaceTerminalRequest{Operation: "open", Cols: 500}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.CloseScope(scope)
	terminal := terminalPTYTest{t: t, pool: &pool, scope: scope, id: opened.ID}
	prefix := username + "@" + sb.ID() + " "
	terminal.waitFor(prefix + terminalWorkspace + " " + suffix)
	terminal.input("printf '%s' \"$BASH_VERSION\" > selected-shell; printf '%s' \"$$\" > terminal-test-pid; export PATH=\"$PWD/.terminal-bin:$PATH\"\n")
	terminal.waitFor(prefix + terminalWorkspace + " " + suffix)
	if data, err := os.ReadFile(filepath.Join(hostWorkspace, "selected-shell")); err != nil || len(data) == 0 {
		t.Fatalf("terminal did not select Bash: %q %v", data, err)
	}

	terminal.input("cd prompt-child\n")
	childPrompt := prefix + terminalWorkspace + "/prompt-child " + suffix
	terminal.waitFor(childPrompt)
	// These are actual Tab bytes submitted through the PTY, followed by Enter.
	// The commands can create the expected files only if readline completes them.
	terminal.input("cat agentflow-tab-fi\t > file-tab-result\n")
	terminal.waitFor(childPrompt)
	if data, err := os.ReadFile(filepath.Join(hostWorkspace, "prompt-child", "file-tab-result")); err != nil || string(data) != "file-completion-ok" {
		t.Fatalf("filename Tab completion failed: %q %v; PTY=%q", data, err, terminal.transcript)
	}
	terminal.input("agentflow-terminal-completion-u\t > command-tab-result\n")
	terminal.waitFor(childPrompt)
	if data, err := os.ReadFile(filepath.Join(hostWorkspace, "prompt-child", "command-tab-result")); err != nil || string(data) != "command-completion-ok" {
		t.Fatalf("command Tab completion failed: %q %v; PTY=%q", data, err, terminal.transcript)
	}
	terminal.input("cd \"$HOME\"\n")
	terminal.waitFor(prefix + "~ " + suffix)
	pool.CloseScope(scope)
	if docker, ok := sb.(*dockerSandbox); ok {
		pid, err := os.ReadFile(filepath.Join(hostWorkspace, "terminal-test-pid"))
		if err != nil || len(pid) == 0 {
			t.Fatalf("read Bash PID: %q %v", pid, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "docker", "exec", docker.name, "sh", "-c", `if kill -0 "$1" 2>/dev/null; then printf alive; else printf stopped; fi`, "terminal-status", string(pid)).CombinedOutput()
		if err != nil || string(output) != "stopped" {
			t.Fatalf("closing Docker PTY left Bash PID %s running: %q %v", pid, output, err)
		}
	}
}

type terminalPTYTest struct {
	t          *testing.T
	pool       *TerminalPool
	scope, id  string
	transcript string
}

func (terminal *terminalPTYTest) input(data string) {
	terminal.t.Helper()
	if _, err := terminal.pool.HandleSandbox(context.Background(), nil, terminal.scope, domain.WorkspaceTerminalRequest{Operation: "input", ID: terminal.id, Data: data}, nil); err != nil {
		terminal.t.Fatal(err)
	}
}

func (terminal *terminalPTYTest) waitFor(want string) {
	terminal.t.Helper()
	var received strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := terminal.pool.HandleSandbox(context.Background(), nil, terminal.scope, domain.WorkspaceTerminalRequest{Operation: "read", ID: terminal.id}, nil)
		if err != nil {
			terminal.t.Fatalf("read terminal: %v; PTY=%q", err, terminal.transcript)
		}
		received.Write(result.Data)
		terminal.transcript += string(result.Data)
		if strings.Contains(received.String(), want) {
			return
		}
		if result.Exited {
			terminal.t.Fatalf("terminal exited before %q; PTY=%q", want, terminal.transcript)
		}
		time.Sleep(10 * time.Millisecond)
	}
	terminal.t.Fatalf("terminal never displayed %q; PTY=%q", want, terminal.transcript)
}

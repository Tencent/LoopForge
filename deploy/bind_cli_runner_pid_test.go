package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const runnerPIDNoCrontab = "#!/bin/sh\nif [ \"${1:-}\" = -l ]; then echo \"no crontab for $(id -un)\" >&2; exit 1; fi\ncat > \"$TEST_CRON_FILE\"\nexit 0\n"

// runnerProcessAlive reports whether pid is a live, non-zombie process.
func runnerProcessAlive(pid int) bool {
	state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(state))
	return len(fields) > 2 && fields[2] != "Z"
}

func waitRunnerProcessGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runnerProcessAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("%s (pid %d) kept running", what, pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func readRunnerPID(t *testing.T, pidFile string) int {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("service.pid = %q", raw)
	}
	return pid
}

func uninstallBindCLI(t *testing.T, env []string) {
	t.Helper()
	cmd := exec.Command("/bin/bash", "bind-cli.sh", "--uninstall")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uninstall failed: %v\n%s", err, out)
	}
}

// The usual case: service.pid still names the running wrapper. Uninstall must
// stop it and remove the PID file.
func TestBindCLIUninstallStopsTheRunnerNamedInServicePID(t *testing.T) {
	_, home, env := installBindCLIWithoutSystemd(t, runnerPIDNoCrontab)
	pidFile := filepath.Join(home, ".agentflow-runner", "service.pid")
	wrapper := readRunnerPID(t, pidFile)
	if !runnerProcessAlive(wrapper) {
		t.Fatalf("service.pid %d is not running before uninstall", wrapper)
	}

	uninstallBindCLI(t, env)

	waitRunnerProcessGone(t, wrapper, "the runner named in service.pid")
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("service.pid not removed: %v", err)
	}
}

// Running --service again stops the runner once before starting the new one.
// The PID named in service.pid is left out of the /proc scan, so the old
// wrapper is stopped only because the checked PID from the file is kept;
// otherwise the reinstall leaves two runners behind. (Uninstall stops twice,
// so it does not notice this on its own.)
func TestBindCLIReinstallStopsTheRunnerNamedInServicePID(t *testing.T) {
	_, home, env := installBindCLIWithoutSystemd(t, runnerPIDNoCrontab)
	pidFile := filepath.Join(home, ".agentflow-runner", "service.pid")
	previous := readRunnerPID(t, pidFile)

	cmd := exec.Command("/bin/bash", "bind-cli.sh",
		"--server", "http://isolated.example",
		"--token", "bind-test",
		"--from", filepath.Join(home, "runner-source"),
		"--bin-dir", filepath.Join(home, "bin"),
		"--service",
	)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second service install failed: %v\n%s", err, out)
	}

	waitRunnerProcessGone(t, previous, "the runner from the first install")
	if current := readRunnerPID(t, pidFile); current == previous || !runnerProcessAlive(current) {
		t.Fatalf("service.pid = %d after reinstall, want a new running wrapper (previous %d)", current, previous)
	}
}

// The @reboot hook runs run-service.sh directly, without the installer's
// launcher that records $!. The wrapper itself must then write its own PID,
// otherwise service.pid keeps the value from before the reboot.
func TestBindCLIRunnerScriptStartedDirectlyRecordsItsOwnPID(t *testing.T) {
	_, home, env := installBindCLIWithoutSystemd(t, runnerPIDNoCrontab)
	script := filepath.Join(home, ".agentflow-runner", "run-service.sh")
	pidFile := filepath.Join(home, ".agentflow-runner", "service.pid")

	// Simulate the reboot: the installer's wrapper is gone and service.pid
	// holds a PID that no longer belongs to it.
	installed := readRunnerPID(t, pidFile)
	if p, err := os.FindProcess(installed); err == nil {
		_ = p.Signal(syscall.SIGTERM)
	}
	waitRunnerProcessGone(t, installed, "the installed wrapper")
	if err := os.WriteFile(pidFile, []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(script)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// TERM rather than KILL, so the wrapper's trap also stops its child.
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGTERM); _, _ = cmd.Process.Wait() })

	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(pidFile)
		if strings.TrimSpace(string(raw)) == strconv.Itoa(cmd.Process.Pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service.pid = %q after a direct start, want the wrapper's pid %d", raw, cmd.Process.Pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A process whose argv[0] is run-service.sh itself counts as the runner too,
// so uninstall finds and stops it through /proc even though service.pid does
// not name it.
func TestBindCLIUninstallStopsARunnerWhoseArgv0IsTheScript(t *testing.T) {
	_, home, env := installBindCLIWithoutSystemd(t, runnerPIDNoCrontab)
	script := filepath.Join(home, ".agentflow-runner", "run-service.sh")

	named := exec.Command("/bin/bash", "-c", `exec -a "$0" sleep 300`, script)
	if err := named.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = named.Process.Kill(); _, _ = named.Process.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", named.Process.Pid))
		if strings.HasPrefix(string(cmdline), script+"\x00") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("argv[0] was not set to the script: %q", cmdline)
		}
		time.Sleep(20 * time.Millisecond)
	}
	exited := make(chan struct{})
	go func() { _, _ = named.Process.Wait(); close(exited) }()

	uninstallBindCLI(t, env)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("uninstall left running a process whose argv[0] is run-service.sh")
	}
}

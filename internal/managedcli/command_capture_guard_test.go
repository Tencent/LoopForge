//go:build linux || darwin

package managedcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const captureFailSecret = "npm_token=guard-secret-value"

func TestCommandCaptureFailingHelperProcess(t *testing.T) {
	if os.Getenv("AGENTFLOW_TEST_CAPTURE_FAIL_HELPER") == "" {
		return
	}
	_, _ = os.Stdout.Write([]byte("partial help\n"))
	_, _ = os.Stderr.Write([]byte(captureFailSecret + "\n"))
	os.Exit(7)
}

// A failing CLI must still hand its captured output back to the caller, but
// the error itself must never embed that output (it can carry credentials) or
// the full binary path, and must keep the exit status reachable via errors.As.
func TestExecuteCommandFailureReturnsOutputButKeepsItOutOfError(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := executeCommand(ctx, os.Args[0], []string{"-test.run=^TestCommandCaptureFailingHelperProcess$"}, dir, helperEnv(t, "AGENTFLOW_TEST_CAPTURE_FAIL_HELPER=1"))
	if err == nil {
		t.Fatal("failing command returned no error")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("exit status not wrapped: %v", err)
	}
	base := filepath.Base(os.Args[0])
	if !strings.HasPrefix(err.Error(), "command "+base+" failed: ") {
		t.Fatalf("error = %q, want prefix with binary base name %q", err, base)
	}
	if strings.Contains(err.Error(), filepath.Dir(os.Args[0])) {
		t.Fatalf("error leaks binary directory: %q", err)
	}
	if strings.Contains(err.Error(), "guard-secret-value") || strings.Contains(err.Error(), "partial help") {
		t.Fatalf("error leaks captured output: %q", err)
	}
	if !strings.Contains(string(data), "partial help\n") || !strings.Contains(string(data), captureFailSecret) {
		t.Fatalf("captured output not returned on failure: %q", data)
	}
	entries, err := os.ReadDir(dir)
	must(t, err)
	if len(entries) != 0 {
		t.Fatal("private output capture retained after failure")
	}
}

// executeCommand must stop its size watcher before returning; otherwise every
// CLI probe would leak a goroutine polling a closed, deleted capture file.
func TestExecuteCommandStopsCaptureWatcher(t *testing.T) {
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := executeCommand(ctx, os.Args[0], []string{"-test.run=^TestCommandCaptureHelperProcess$"}, t.TempDir(), helperEnv(t, "AGENTFLOW_TEST_CAPTURE_HELPER=normal"))
	must(t, err)
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d after executeCommand, want <= %d (watcher leaked)", runtime.NumGoroutine(), before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Stat failures (for example a capture file closed under the watcher) are not
// size evidence: the watcher must keep running quietly instead of crashing or
// canceling the command.
func TestWatchCaptureSizeIgnoresStatErrors(t *testing.T) {
	file := newCaptureFile(t)
	must(t, file.Close())
	canceled := make(chan struct{}, 1)
	stop := watchCaptureSize(file, func() { canceled <- struct{}{} })
	time.Sleep(50 * time.Millisecond)
	stop()
	select {
	case <-canceled:
		t.Fatal("watcher canceled on a Stat error")
	default:
	}
}

func TestReadCapturedOutputReportsStatError(t *testing.T) {
	file := newCaptureFile(t)
	_, err := file.WriteString("help")
	must(t, err)
	must(t, file.Close())
	data, err := readCapturedOutput(file)
	if err == nil || errors.Is(err, errCommandOutputTooLarge) || data != nil {
		t.Fatalf("data=%q err=%v, want the underlying Stat error", data, err)
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("err = %v, want os.ErrClosed", err)
	}
}

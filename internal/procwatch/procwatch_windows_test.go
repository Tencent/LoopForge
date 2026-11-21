package procwatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const windowsProcwatchTestMode = "AGENTFLOW_TEST_PROCWATCH_MODE"

func TestWindowsProcwatchStopsProcessTree(t *testing.T) {
	t.Run("cancel", func(t *testing.T) { testWindowsProcwatchStopsProcessTree(t, 0) })
	t.Run("timeout", func(t *testing.T) { testWindowsProcwatchStopsProcessTree(t, 2*time.Second) })
}

func testWindowsProcwatchStopsProcessTree(t *testing.T, timeout time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan int, 1)
	terminal := make(chan Event, 1)
	ctx = WithObserver(ctx, func(e Event) {
		if raw, ok := strings.CutPrefix(e.Message, "prepare：helper-pid "); e.Kind == Progress && ok {
			if pid, err := strconv.Atoi(raw); err == nil {
				ready <- pid
			}
		}
		if e.Kind == Failed || e.Kind == Completed {
			terminal <- e
		}
	})
	type result struct {
		tail    string
		err     error
		elapsed time.Duration
	}
	finished := make(chan result, 1)
	go func() {
		started := time.Now()
		tail, err := Run(ctx, func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsProcwatchProcessHelper$")
			cmd.Env = append(os.Environ(), windowsProcwatchTestMode+"=parent")
			return cmd
		}, Options{Stage: "prepare", Label: "prepare", Timeout: timeout, TimeoutEnv: "AGENTFLOW_TEST_PROCWATCH_TIMEOUT"})
		finished <- result{tail: tail, err: err, elapsed: time.Since(started)}
	}()
	var childPID int
	select {
	case childPID = <-ready:
	case got := <-finished:
		t.Fatalf("watch ended before starting a helper: %v (%q)", got.err, got.tail)
	case <-time.After(8 * time.Second):
		t.Fatal("watched process did not start its helper")
	}

	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(childPID))
	if err != nil {
		t.Fatalf("open the test helper: %v", err)
	}
	defer windows.CloseHandle(child)
	defer func() { _ = windows.TerminateProcess(child, 1) }()
	if timeout == 0 {
		cancel()
	}
	select {
	case got := <-finished:
		if got.err == nil || Stopped(got.err) != (timeout > 0) {
			t.Fatalf("incorrect stop result: %v", got.err)
		}
		if timeout > 0 && !strings.Contains(got.err.Error(), "AGENTFLOW_TEST_PROCWATCH_TIMEOUT") {
			t.Fatalf("timeout did not identify its setting: %v", got.err)
		}
		if got.elapsed >= waitDelay || !strings.Contains(got.tail, "helper-pid ") {
			t.Fatalf("output did not close promptly: %s (%q)", got.elapsed, got.tail)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("watched command did not return")
	}
	if last := <-terminal; last.Kind != Failed || last.Stage != "prepare" {
		t.Fatalf("unexpected terminal event: %+v", last)
	}
	status, err := windows.WaitForSingleObject(child, 2000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("helper survived the watched command: status=%d err=%v", status, err)
	}
}

// TestWindowsProcwatchProcessHelper creates a native descendant that inherits the watched pipes.
func TestWindowsProcwatchProcessHelper(t *testing.T) {
	switch os.Getenv(windowsProcwatchTestMode) {
	case "parent":
		cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsProcwatchProcessHelper$")
		cmd.Env = append(os.Environ(), windowsProcwatchTestMode+"=child")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("helper-pid %d\n", cmd.Process.Pid)
		_ = cmd.Wait()
	case "child":
		time.Sleep(time.Minute)
	}
}

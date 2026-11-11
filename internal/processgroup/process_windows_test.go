package processgroup

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWindowsProcessTreeCancellation verifies cancellation reaches a real child process.
func TestWindowsProcessTreeCancellation(t *testing.T) {
	for _, mode := range []string{"parent", "parent-exit"} {
		t.Run(mode, func(t *testing.T) { testWindowsProcessTreeCancellation(t, mode) })
	}
}

func testWindowsProcessTreeCancellation(t *testing.T, mode string) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessTreeHelper$")
	cmd.Env = append(os.Environ(), "LOOPFORGE_PROCESS_TREE_TEST="+mode)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	waited := false
	defer func() {
		if !waited {
			_ = Kill(cmd.Process.Pid)
			_ = cmd.Wait()
		}
	}()
	childReady := make(chan string, 1)
	drained := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(out)
		if scanner.Scan() {
			childReady <- scanner.Text()
		} else {
			childReady <- ""
		}
		for scanner.Scan() {
		}
		drained <- scanner.Err()
	}()
	var childPID int
	select {
	case line := <-childReady:
		childPID, err = strconv.Atoi(strings.TrimSpace(line))
		if err != nil || childPID <= 0 {
			t.Fatalf("invalid child process id %q: %v", line, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not start its child")
	}
	if mode == "parent" {
		if !Alive(childPID) {
			t.Fatal("child exited before cancellation")
		}
		if err := Kill(cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
	}
	// The child inherits stdout, so this reaches EOF only after the job is reaped.
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a descendant kept the stdout pipe open")
	}
	err = cmd.Wait()
	waited = true
	if (err == nil) != (mode == "parent-exit") {
		t.Fatalf("unexpected parent exit for %s: %v", mode, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for Alive(childPID) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if Alive(childPID) {
		t.Fatal("job left the child process alive")
	}
}

func TestWindowsProcessKillLeavesUnownedProcessAlive(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessTreeHelper$")
	cmd.Env = append(os.Environ(), "LOOPFORGE_PROCESS_TREE_TEST=child")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	if err := Kill(cmd.Process.Pid); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("unowned process must be rejected: %v", err)
	}
	if !Alive(cmd.Process.Pid) {
		t.Fatal("unowned process was terminated")
	}
}

// TestWindowsProcessTreeHelper creates a deterministic parent and child without external tools.
func TestWindowsProcessTreeHelper(t *testing.T) {
	switch os.Getenv("LOOPFORGE_PROCESS_TREE_TEST") {
	case "parent", "parent-exit":
		child := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessTreeHelper$")
		child.Env = append(os.Environ(), "LOOPFORGE_PROCESS_TREE_TEST=child")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Println("failed")
			os.Exit(2)
		}
		fmt.Println(child.Process.Pid)
		if os.Getenv("LOOPFORGE_PROCESS_TREE_TEST") == "parent-exit" {
			return
		}
		_ = child.Wait()
		os.Exit(3)
	case "child":
		time.Sleep(time.Hour)
		os.Exit(4)
	}
}

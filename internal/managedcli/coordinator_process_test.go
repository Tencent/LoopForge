//go:build linux || darwin

package managedcli

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// One child-process fixture serves lock/crash and daily-claim tests. All network
// work is replaced; these env keys exist only in the test executable.
func TestManagedProcessHelper(t *testing.T) {
	mode, root := os.Getenv("MANAGED_TEST_MODE"), os.Getenv("MANAGED_TEST_ROOT")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wait := func() { _, _ = bufio.NewReader(os.Stdin).ReadString('\n') }
	switch mode {
	case "claim":
		lease, err := BeginClaim(ctx, root)
		must(t, err)
		defer lease.Close()
		fmt.Println("ready")
		wait()
	case "maintenance":
		must(t, WithMaintenance(ctx, root, func() error { fmt.Println("ready"); wait(); return nil }))
	case "check", "crash-check":
		m, err := NewManager(root)
		must(t, err)
		m.Now = func() time.Time { return time.Date(2026, 1, 1, 8, 0, 0, 0, beijing) }
		m.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			file, err := os.OpenFile(filepath.Join(root, "attempts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			must(t, err)
			_, err = file.WriteString("attempt\n")
			must(t, err)
			must(t, file.Close())
			if mode == "crash-check" {
				fmt.Println("ready") // Check has persisted LastCheck before registry IO.
				wait()
			}
			return failingTransport{}.RoundTrip(r)
		})}
		_, _ = m.Check(ctx) // Intentional offline error still consumes today's claim.
	}
}

type testChild struct {
	cmd   *exec.Cmd
	lines chan string
}

func startChild(t *testing.T, root, mode string) *testChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestManagedProcessHelper$")
	cmd.Env = append(os.Environ(), "MANAGED_TEST_ROOT="+root, "MANAGED_TEST_MODE="+mode)
	stdin, err := cmd.StdinPipe()
	must(t, err)
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	cmd.Stderr = os.Stderr
	must(t, cmd.Start())
	child := &testChild{cmd: cmd, lines: make(chan string, 8)}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			child.lines <- scanner.Text()
		}
		close(child.lines)
	}()
	t.Cleanup(func() {
		stdin.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return child
}

func (c *testChild) ready(t *testing.T) {
	t.Helper()
	select {
	case line := <-c.lines:
		if line != "ready" {
			t.Fatalf("child output=%q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child not ready")
	}
}

func (c *testChild) kill(t *testing.T) {
	t.Helper()
	must(t, c.cmd.Process.Kill())
	_ = c.cmd.Wait() // Intentional SIGKILL is the crash under test.
}

func TestCrossProcessDrainAndCrashRecovery(t *testing.T) {
	for _, crashWriterFirst := range []bool{false, true} {
		root := t.TempDir()
		reader := startChild(t, root, "claim")
		reader.ready(t)
		writer := startChild(t, root, "maintenance")
		waitForMaintenanceIntent(t, root)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		if err := claimAndClose(ctx, root); err == nil {
			t.Fatal("new claim bypassed writer intent")
		}
		cancel()
		select {
		case line := <-writer.lines:
			t.Fatalf("writer bypassed active claim/task: %q", line)
		default:
		}
		if crashWriterFirst {
			writer.kill(t)
			must(t, claimAndClose(context.Background(), root))
			reader.kill(t)
		} else {
			reader.kill(t)
			writer.ready(t)
			writer.kill(t)
		}
		must(t, WithMaintenance(context.Background(), root, func() error { return nil }))
	}
}

func TestDailyClaimAcrossProcessesAndCrash(t *testing.T) {
	for _, crash := range []bool{false, true} {
		root := t.TempDir()
		if crash {
			child := startChild(t, root, "crash-check")
			child.ready(t)
			state, err := ReadState(root)
			must(t, err)
			if state.LastCheck.IsZero() {
				t.Fatal("external work started without durable claim")
			}
			child.kill(t)
		}
		children := []*testChild{}
		for i := 0; i < 4; i++ {
			children = append(children, startChild(t, root, "check"))
		}
		for _, child := range children {
			must(t, child.cmd.Wait())
		}
		data, err := os.ReadFile(filepath.Join(root, "attempts"))
		must(t, err)
		want := "attempt\nattempt\nattempt\n" // One check visits all three providers.
		if crash {
			want = "attempt\n" // The first request was interrupted after the claim.
		}
		if string(data) != want {
			t.Fatalf("duplicate daily attempt: %q", data)
		}
	}
}

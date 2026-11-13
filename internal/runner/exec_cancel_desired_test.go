package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	flowruntime "agentflow/internal/runtime"
)

func TestRunnerTaskCancelDesired(t *testing.T) {
	cases := map[string]bool{
		"cancel":  true,
		"CANCEL":  true,
		"Cancel":  true,
		"":        false,
		"run":     false,
		"pause":   false,
		" cancel": false,
	}
	for state, want := range cases {
		if got := runnerTaskCancelDesired(state); got != want {
			t.Errorf("runnerTaskCancelDesired(%q) = %v, want %v", state, got, want)
		}
	}
}

// platformCancelTestDaemon returns a daemon whose event endpoint counts the
// "cancel requested by platform" system lines it receives.
func platformCancelTestDaemon(t *testing.T) (*Daemon, *atomic.Int64) {
	t.Helper()
	var cancelLines atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events") {
			var body struct {
				Events []domain.RunnerTaskEvent `json:"events"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, ev := range body.Events {
				if ev.Type == "system" && ev.Data == "cancel requested by platform" {
					cancelLines.Add(1)
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return &Daemon{
		cfg:             Config{DefaultWorkspaceRoot: t.TempDir()},
		client:          NewClient(server.URL),
		controls:        map[string]chan string{},
		pendingControls: map[string]string{},
	}, &cancelLines
}

// deliverControl pushes desiredState to the task control channel and waits
// until the exec loop has consumed it.
func deliverControl(t *testing.T, d *Daemon, ch chan string, taskID, desiredState string) {
	t.Helper()
	d.signalTaskControl(taskID, desiredState)
	deadline := time.Now().Add(10 * time.Second)
	for len(ch) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("control %q was not consumed", desiredState)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExecuteProcessIgnoresNonCancelDesiredState(t *testing.T) {
	d, cancelLines := platformCancelTestDaemon(t)
	const taskID = "ignore-run-state"
	ch := d.registerTaskControl(taskID)
	defer d.unregisterTaskControl(taskID)
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	go func() {
		waitForFile(t, started)
		// A non-cancel desired state must not terminate the running child.
		deliverControl(t, d, ch, taskID, "run")
		_ = os.WriteFile(release, nil, 0o644)
	}()
	script := `touch "$1"; i=0; while [ ! -f "$2" ] && [ $i -lt 3000 ]; do sleep 0.01; i=$((i+1)); done; [ -f "$2" ]`
	result := d.executeProcess(context.Background(), domain.RunnerTask{ID: taskID}, []string{"sh", "-c", script, "sh", started, release})
	if result.status != domain.RunnerTaskSuccess {
		t.Fatalf("non-cancel desired state should not cancel the task: %+v", result)
	}
	if got := cancelLines.Load(); got != 0 {
		t.Fatalf("cancel lines = %d, want 0", got)
	}
}

// gatedCancelSandbox blocks until its context is canceled, reports that, and
// then waits for release before returning so the test can deliver more
// control messages while the exec loop is still running.
type gatedCancelSandbox struct {
	workspace string
	started   chan struct{}
	canceled  chan struct{}
	release   chan struct{}
}

func (s *gatedCancelSandbox) ID() string                  { return "gated" }
func (s *gatedCancelSandbox) Workspace() string           { return s.workspace }
func (s *gatedCancelSandbox) Close(context.Context) error { return nil }
func (s *gatedCancelSandbox) Exec(ctx context.Context, _ flowruntime.CommandSpec, _ flowruntime.EventSink) (string, error) {
	close(s.started)
	<-ctx.Done()
	close(s.canceled)
	<-s.release
	return "", ctx.Err()
}

func TestExecuteSandboxProcessPlatformCancelHandledOnce(t *testing.T) {
	d, cancelLines := platformCancelTestDaemon(t)
	const taskID = "sandbox-cancel-once"
	ch := d.registerTaskControl(taskID)
	defer d.unregisterTaskControl(taskID)
	sb := &gatedCancelSandbox{workspace: t.TempDir(), started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	go func() {
		<-sb.started
		// A non-cancel desired state is ignored and must not cancel the sandbox.
		deliverControl(t, d, ch, taskID, "run")
		select {
		case <-sb.canceled:
			t.Error("non-cancel desired state canceled the sandbox")
		default:
		}
		deliverControl(t, d, ch, taskID, "Cancel")
		<-sb.canceled
		// The exec loop is still running: a repeated cancel must be a no-op.
		deliverControl(t, d, ch, taskID, domain.RunnerTaskDesiredCancel)
		close(sb.release)
	}()
	result := d.executeSandboxProcess(context.Background(), domain.RunnerTask{ID: taskID}, sb, flowruntime.CommandSpec{Argv: []string{"x"}})
	if result.status != domain.RunnerTaskCanceled || result.errMsg != "runner stopped" {
		t.Fatalf("platform cancel result = %+v", result)
	}
	if got := cancelLines.Load(); got != 1 {
		t.Fatalf("cancel lines = %d, want exactly 1", got)
	}
}

func TestExecuteProcessPlatformCancelStopsChild(t *testing.T) {
	d, cancelLines := platformCancelTestDaemon(t)
	const taskID = "process-platform-cancel"
	ch := d.registerTaskControl(taskID)
	defer d.unregisterTaskControl(taskID)
	started := filepath.Join(t.TempDir(), "started")
	go func() {
		waitForFile(t, started)
		deliverControl(t, d, ch, taskID, "CANCEL")
	}()
	begin := time.Now()
	result := d.executeProcess(context.Background(), domain.RunnerTask{ID: taskID}, []string{"sh", "-c", `touch "$1"; exec sleep 60`, "sh", started})
	if result.status != domain.RunnerTaskCanceled || result.errMsg != "runner stopped" {
		t.Fatalf("platform cancel result = %+v", result)
	}
	// The child sleeps for 60s; returning well before that proves the
	// platform cancel terminated the process group instead of waiting it out.
	if elapsed := time.Since(begin); elapsed > cancelGrace {
		t.Fatalf("platform cancel took %s, child was not terminated", elapsed)
	}
	if got := cancelLines.Load(); got != 1 {
		t.Fatalf("cancel lines = %d, want 1", got)
	}
}

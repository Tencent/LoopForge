package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	flowruntime "agentflow/internal/runtime"
)

const platformCancelLine = "cancel requested by platform"

// platformCancelServer is a minimal runner API: it answers the HTTP control
// fallback with a configurable desired state and records uploaded events by
// seq (the batcher may resend a batch, the server dedupes on seq).
type platformCancelServer struct {
	*httptest.Server
	mu           sync.Mutex
	desiredState string
	controlCalls int
	events       map[int64]domain.RunnerTaskEvent
}

func newPlatformCancelServer(t *testing.T, desiredState string) *platformCancelServer {
	t.Helper()
	s := &platformCancelServer{desiredState: desiredState, events: map[int64]domain.RunnerTaskEvent{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/control"):
			s.mu.Lock()
			s.controlCalls++
			state := s.desiredState
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(RenewResponse{DesiredState: state})
		case strings.HasSuffix(r.URL.Path, "/events"):
			var body struct {
				Events []domain.RunnerTaskEvent `json:"events"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			for _, event := range body.Events {
				s.events[event.Seq] = event
			}
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *platformCancelServer) cancelLines() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, event := range s.events {
		if event.Type == "system" && event.Data == platformCancelLine {
			count++
		}
	}
	return count
}

func (s *platformCancelServer) controls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controlCalls
}

func newPlatformCancelDaemon(t *testing.T, server *platformCancelServer) *Daemon {
	t.Helper()
	return &Daemon{
		cfg:             Config{DefaultWorkspaceRoot: t.TempDir()},
		client:          NewClient(server.URL),
		controls:        map[string]chan string{},
		pendingControls: map[string]string{},
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %s", path)
}

// sendControl delivers a control frame like the WebSocket reader does, but
// fails instead of blocking forever if the execution loop stopped reading.
// It calls t.Fatalf, so it must only be used from the test goroutine.
func sendControl(t *testing.T, ch chan<- string, desiredState string) {
	t.Helper()
	select {
	case ch <- desiredState:
	case <-time.After(30 * time.Second):
		t.Fatalf("control frame %q was not consumed", desiredState)
	}
}

type platformCancelResult struct {
	result execResult
	took   time.Duration
}

func runAsync(fn func() execResult) <-chan platformCancelResult {
	out := make(chan platformCancelResult, 1)
	started := time.Now()
	go func() {
		result := fn()
		out <- platformCancelResult{result: result, took: time.Since(started)}
	}()
	return out
}

func awaitResult(t *testing.T, ch <-chan platformCancelResult) platformCancelResult {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(30 * time.Second):
		t.Fatal("execution did not finish after platform cancel")
		return platformCancelResult{}
	}
}

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

// A WebSocket control frame asking to cancel (any letter case) terminates the
// local process and reports the run as canceled.
func TestExecuteProcessCancelsOnControlFrame(t *testing.T) {
	server := newPlatformCancelServer(t, "")
	d := newPlatformCancelDaemon(t, server)
	controlCh := d.registerTaskControl("ws-cancel")
	marker := filepath.Join(t.TempDir(), "spawned")

	done := runAsync(func() execResult {
		return d.executeProcess(context.Background(), domain.RunnerTask{ID: "ws-cancel"}, []string{"sh", "-c", `touch "$1"; exec sleep 30`, "sh", marker})
	})
	waitForFile(t, marker)
	sendControl(t, controlCh, "CANCEL")

	got := awaitResult(t, done)
	if got.result.status != domain.RunnerTaskCanceled || got.result.errMsg != "runner stopped" {
		t.Fatalf("result=%+v", got.result)
	}
	if got.took > 20*time.Second {
		t.Fatalf("cancel took %s, process was not terminated by the platform cancel", got.took)
	}
	if n := server.cancelLines(); n != 1 {
		t.Fatalf("cancel lines=%d, want exactly 1", n)
	}
}

// Control frames whose desired state is not cancel are ignored: the process
// keeps running and finishes successfully without a cancel line.
func TestExecuteProcessIgnoresNonCancelControlFrames(t *testing.T) {
	server := newPlatformCancelServer(t, "")
	d := newPlatformCancelDaemon(t, server)
	controlCh := d.registerTaskControl("ws-ignore")
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned")
	gate := filepath.Join(dir, "gate")

	done := runAsync(func() execResult {
		return d.executeProcess(context.Background(), domain.RunnerTask{ID: "ws-ignore"}, []string{"sh", "-c", `touch "$1"; while [ ! -f "$2" ]; do sleep 0.05; done; echo finished`, "sh", marker, gate})
	})
	waitForFile(t, marker)
	// The channel has capacity 1, so once the last send returns the loop has
	// already consumed and handled every earlier frame.
	for _, state := range []string{"run", "pause", " cancel", "running", ""} {
		sendControl(t, controlCh, state)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got := awaitResult(t, done)
	if got.result.status != domain.RunnerTaskSuccess || got.result.result != "finished" {
		t.Fatalf("result=%+v", got.result)
	}
	if n := server.cancelLines(); n != 0 {
		t.Fatalf("cancel lines=%d, want 0", n)
	}
}

// When the WebSocket frame is missed, the HTTP control fallback still cancels
// the process.
func TestExecuteProcessCancelsOnHTTPControlFallback(t *testing.T) {
	server := newPlatformCancelServer(t, "Cancel")
	d := newPlatformCancelDaemon(t, server)
	marker := filepath.Join(t.TempDir(), "spawned")

	done := runAsync(func() execResult {
		return d.executeProcess(context.Background(), domain.RunnerTask{ID: "http-cancel"}, []string{"sh", "-c", `touch "$1"; exec sleep 30`, "sh", marker})
	})
	waitForFile(t, marker)

	got := awaitResult(t, done)
	if got.result.status != domain.RunnerTaskCanceled {
		t.Fatalf("result=%+v", got.result)
	}
	if got.took > 20*time.Second {
		t.Fatalf("cancel took %s, process was not terminated by the platform cancel", got.took)
	}
	if calls := server.controls(); calls < 1 {
		t.Fatalf("control polls=%d, want the HTTP fallback to be polled", calls)
	}
	if n := server.cancelLines(); n != 1 {
		t.Fatalf("cancel lines=%d, want exactly 1", n)
	}
}

type platformCancelSandbox struct {
	workspace string
	started   chan struct{}
	release   chan struct{}
	// canceled, when set, is closed once Exec observes cancellation; Exec
	// then keeps running until release is closed, like a slow-to-stop tool.
	canceled chan struct{}
}

func (s *platformCancelSandbox) ID() string                  { return "platform-cancel" }
func (s *platformCancelSandbox) Workspace() string           { return s.workspace }
func (s *platformCancelSandbox) Close(context.Context) error { return nil }
func (s *platformCancelSandbox) Exec(ctx context.Context, _ flowruntime.CommandSpec, _ flowruntime.EventSink) (string, error) {
	close(s.started)
	select {
	case <-ctx.Done():
		if s.canceled != nil {
			close(s.canceled)
			<-s.release
		}
		return "", ctx.Err()
	case <-s.release:
		return "sandbox finished", nil
	}
}

func newPlatformCancelSandbox(t *testing.T) *platformCancelSandbox {
	return &platformCancelSandbox{workspace: t.TempDir(), started: make(chan struct{}), release: make(chan struct{})}
}

// A cancel frame cancels the sandbox execution exactly once even when the
// platform repeats it before the sandbox has stopped.
func TestExecuteSandboxProcessCancelsOnControlFrameOnce(t *testing.T) {
	server := newPlatformCancelServer(t, "")
	d := newPlatformCancelDaemon(t, server)
	controlCh := d.registerTaskControl("sandbox-ws-cancel")
	sandbox := newPlatformCancelSandbox(t)
	sandbox.canceled = make(chan struct{})

	done := runAsync(func() execResult {
		return d.executeSandboxProcess(context.Background(), domain.RunnerTask{ID: "sandbox-ws-cancel"}, sandbox, flowruntime.CommandSpec{Argv: []string{"x"}})
	})
	<-sandbox.started
	sendControl(t, controlCh, "Cancel")
	<-sandbox.canceled
	// The sandbox has not returned yet, so the loop keeps receiving frames;
	// a repeated cancel must neither log again nor cancel again. The extra
	// empty frame guarantees the repeated one was consumed (capacity 1).
	sendControl(t, controlCh, "CANCEL")
	sendControl(t, controlCh, "")
	close(sandbox.release)

	got := awaitResult(t, done)
	if got.result.status != domain.RunnerTaskCanceled || got.result.errMsg != "runner stopped" {
		t.Fatalf("result=%+v", got.result)
	}
	if n := server.cancelLines(); n != 1 {
		t.Fatalf("cancel lines=%d, want exactly 1", n)
	}
}

func TestExecuteSandboxProcessIgnoresNonCancelControlFrames(t *testing.T) {
	server := newPlatformCancelServer(t, "")
	d := newPlatformCancelDaemon(t, server)
	controlCh := d.registerTaskControl("sandbox-ws-ignore")
	sandbox := newPlatformCancelSandbox(t)

	done := runAsync(func() execResult {
		return d.executeSandboxProcess(context.Background(), domain.RunnerTask{ID: "sandbox-ws-ignore"}, sandbox, flowruntime.CommandSpec{Argv: []string{"x"}})
	})
	<-sandbox.started
	for _, state := range []string{"run", "pause", "cancelled", "running", ""} {
		sendControl(t, controlCh, state)
	}
	close(sandbox.release)

	got := awaitResult(t, done)
	if got.result.status != domain.RunnerTaskSuccess || got.result.result != "sandbox finished" {
		t.Fatalf("result=%+v", got.result)
	}
	if n := server.cancelLines(); n != 0 {
		t.Fatalf("cancel lines=%d, want 0", n)
	}
}

// The HTTP control fallback cancels the sandbox once even though every later
// poll still answers cancel.
func TestExecuteSandboxProcessCancelsOnHTTPControlFallback(t *testing.T) {
	server := newPlatformCancelServer(t, "CANCEL")
	d := newPlatformCancelDaemon(t, server)
	sandbox := newPlatformCancelSandbox(t)
	sandbox.canceled = make(chan struct{})
	// Keep the sandbox running for a few more HTTP polls after it was canceled:
	// polls that keep reporting cancel must not log or cancel again.
	go func() {
		<-sandbox.canceled
		deadline := time.Now().Add(30 * time.Second)
		for server.controls() < 3 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		close(sandbox.release)
	}()

	got := awaitResult(t, runAsync(func() execResult {
		return d.executeSandboxProcess(context.Background(), domain.RunnerTask{ID: "sandbox-http-cancel"}, sandbox, flowruntime.CommandSpec{Argv: []string{"x"}})
	}))
	if got.result.status != domain.RunnerTaskCanceled {
		t.Fatalf("result=%+v", got.result)
	}
	if n := server.cancelLines(); n != 1 {
		t.Fatalf("cancel lines=%d, want exactly 1", n)
	}
}

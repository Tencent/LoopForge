package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRunnerNodeCheckpointCreateDiffAndRestore(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/start") {
			t.Errorf("unexpected runner call %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	d.client = NewClient(server.URL)
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	git("init", "-q")
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "baseline")
	task := domain.RunnerTask{ID: "checkpoint", RunID: request.RunID, Workspace: "runs/prepared/workspace", NodeCheckpointRunID: request.RunID, NodeCheckpointAttempt: 1, NodeCheckpointLease: "lease-a"}
	created := d.executeNodeCheckpointCreate(context.Background(), task)
	if created.status != domain.RunnerTaskSuccess {
		t.Fatal(created.errMsg)
	}
	var start domain.NodeCheckpoint
	if err := json.Unmarshal([]byte(created.result), &start); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.Operation, request.NodeCheckpoint, request.NodeCheckpointEnd = domain.RunnerWorkspaceOperationNodeCheckpointDiff, &start, nil
	response := d.readRunnerWorkspace(context.Background(), request)
	if response.Status != 200 || !strings.Contains(string(response.Payload), "+after") || !strings.Contains(string(response.Payload), `"live":true`) {
		t.Fatalf("live diff: %+v", response)
	}
	refs, err := exec.Command("git", "-C", root, "for-each-ref", "--format=%(refname)", "refs/loopforge/checkpoints").Output()
	if err != nil || len(strings.Fields(string(refs))) != 1 {
		t.Fatalf("live diff leaked a ref: %q, %v", refs, err)
	}
	request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointCreate
	request.CheckpointLease = task.NodeCheckpointLease
	request.Repositories = []domain.RepositoryContext{{}}
	response = d.readRunnerWorkspace(context.Background(), request)
	if response.Status != 200 {
		t.Fatalf("capture: %+v", response)
	}
	var end domain.NodeCheckpoint
	if err := json.Unmarshal(response.Payload, &end); err != nil {
		t.Fatal(err)
	}
	request.Operation, request.NodeCheckpoint, request.NodeCheckpointEnd = domain.RunnerWorkspaceOperationNodeCheckpointDiff, &start, &end
	response = d.readRunnerWorkspace(context.Background(), request)
	if response.Status != 200 || !strings.Contains(string(response.Payload), "+after") {
		t.Fatalf("diff: %+v", response)
	}
	request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointRestore
	response = d.readRunnerWorkspace(context.Background(), request)
	if response.Status != 200 {
		t.Fatalf("restore: %+v", response)
	}
	contents, err := os.ReadFile(file)
	if err != nil || string(contents) != "before\n" {
		t.Fatalf("restored %q: %v", contents, err)
	}
}

func TestRunnerNodeCheckpointReservationBlocksWorkspaceWriters(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runners/workspace/checkpoint-lease" {
			t.Errorf("unexpected runner call %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	d.client = NewClient(server.URL)
	request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointReserve
	request.CheckpointLease = "lease-a"
	if response := d.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusOK {
		t.Fatalf("reserve: %+v", response)
	}
	request.Operation = domain.RunnerWorkspaceOperationWrite
	request.NodeCheckpointsEnabled = true
	request.ConcurrentEdit = true
	request.Path, request.Content = "new-file", "should not be written"
	if response := d.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusConflict {
		t.Fatalf("write was not blocked: %+v", response)
	}
	if _, err := os.Stat(filepath.Join(root, "new-file")); !os.IsNotExist(err) {
		t.Fatalf("write changed workspace: %v", err)
	}
	request.Operation = domain.RunnerWorkspaceOperationPrepare
	request.CheckpointLease = ""
	if response := d.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusConflict {
		t.Fatalf("preparation was not blocked: %+v", response)
	}
	for _, operation := range []string{domain.RunnerWorkspaceOperationRestore, domain.RunnerWorkspaceOperationCheckpointCommit, domain.RunnerWorkspaceOperationArtifacts} {
		request.Operation = operation
		if response := d.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusConflict {
			t.Fatalf("%s was not blocked: %+v", operation, response)
		}
	}
	request.Operation = domain.RunnerWorkspaceOperationTerminal
	request.TerminalOwner = 7
	request.Terminal = &domain.WorkspaceTerminalRequest{Operation: "open"}
	if response := d.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusConflict {
		t.Fatalf("terminal was not blocked: %+v", response)
	}
	request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointRelease
	request.CheckpointLease = "stale-lease"
	if response := d.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusOK {
		t.Fatalf("release: %+v", response)
	}
	if reserved, ok := d.nodeCheckpointReservations.Load(requestGateKey(request)); !ok || reserved != "lease-a" {
		t.Fatal("stale lease released a newer reservation")
	}
}

func TestRunnerCheckpointFenceSurvivesDaemonRestart(t *testing.T) {
	d, request, _ := runnerPreparationFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	d.client = NewClient(server.URL)
	request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointReserve
	request.CheckpointLease = "first-token"
	if response := d.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusOK {
		t.Fatalf("reserve: %+v", response)
	}
	marker, err := os.ReadFile(d.nodeCheckpointFencePath(requestGateKey(request)))
	if err != nil || strings.Contains(string(marker), request.CheckpointLease) {
		t.Fatalf("invalid persisted fence: %q, %v", marker, err)
	}
	// A restarted daemon has no in-memory state even though the old task may
	// still be running. It cannot take over or remove the old reservation.
	restarted := &Daemon{cfg: d.cfg, client: NewClient(server.URL)}
	request.CheckpointLease = "next-token"
	if response := restarted.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusConflict {
		t.Fatalf("restart took over fence: %+v", response)
	}
	request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointRelease
	if response := restarted.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusOK {
		t.Fatalf("stale release: %+v", response)
	}
	if _, err := os.Stat(d.nodeCheckpointFencePath(requestGateKey(request))); err != nil {
		t.Fatalf("stale release removed fence: %v", err)
	}
	request.Operation, request.Path, request.Content = domain.RunnerWorkspaceOperationWrite, "new-file", "blocked"
	if response := restarted.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusConflict {
		t.Fatalf("restart allowed a workspace write: %+v", response)
	}
	request.Operation = domain.RunnerWorkspaceOperationPrepare
	if response := restarted.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusConflict {
		t.Fatalf("restart allowed workspace preparation: %+v", response)
	}
	request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointRelease
	request.CheckpointLease = "first-token"
	if response := d.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusOK {
		t.Fatalf("owner release: %+v", response)
	}
	request.Operation, request.CheckpointLease = domain.RunnerWorkspaceOperationNodeCheckpointReserve, "next-token"
	if response := restarted.readRunnerWorkspace(context.Background(), request); response.Status != http.StatusOK {
		t.Fatalf("new reserve after release: %+v", response)
	}
}

func TestChildCheckpointRejectsParentTerminal(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	opened, err := d.workspaceTerminals.Handle(root, request.RunID+":1", domain.WorkspaceTerminalRequest{Operation: "open"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.workspaceTerminals.Handle(root, request.RunID+":1", domain.WorkspaceTerminalRequest{Operation: "close", ID: opened.ID}, nil)
	request.WorkspaceRunID, request.RunID = request.RunID, "child"
	request.WorkspaceLayout = domain.RunnerWorkspaceLayoutShared
	request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointReserve
	if response := d.readRunnerWorkspace(context.Background(), request); response.Code != domain.WorkspaceErrorTerminalActive {
		t.Fatalf("child reserve ignored parent terminal: %+v", response)
	}
}

func TestCancelledRunnerTaskKeepsCheckpointFence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/start") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	root := t.TempDir()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}, client: NewClient(server.URL), controls: map[string]chan string{}, pendingControls: map[string]string{}}
	key := "cancelled"
	if err := d.reserveNodeCheckpointFence(key, "lease"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := domain.RunnerTask{ID: "cancelled-task", RunID: key, Workspace: "runs/cancelled/workspace", NodeCheckpointLease: "lease", Command: "printf ready > started; sleep 30"}
	atomic.AddInt32(&d.running, 1)
	done := make(chan struct{})
	go func() { d.executeTask(ctx, task); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, task.Workspace, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled task did not finish")
	}
	if _, ok := d.nodeCheckpointReservations.Load(key); ok {
		t.Fatal("cancelled task left a releasable reservation")
	}
	if err := d.releaseNodeCheckpointFence(key, "lease"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.nodeCheckpointFencePath(key)); err != nil {
		t.Fatalf("cancelled task lost durable fence: %v", err)
	}
}

func TestCancelledRunnerTaskStartKeepsCheckpointFence(t *testing.T) {
	started := make(chan struct{}, 1)
	releaseStart := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/start") {
			started <- struct{}{}
			<-releaseStart
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL), controls: map[string]chan string{}, pendingControls: map[string]string{}}
	if err := d.reserveNodeCheckpointFence("start-cancelled", "lease"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := domain.RunnerTask{ID: "start-cancelled-task", RunID: "start-cancelled", NodeCheckpointLease: "lease", Command: "true"}
	atomic.AddInt32(&d.running, 1)
	done := make(chan struct{})
	go func() { d.executeTask(ctx, task); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("task start request did not arrive")
	}
	cancel()
	close(releaseStart)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled task did not finish")
	}
	if _, ok := d.nodeCheckpointReservations.Load(task.RunID); ok {
		t.Fatal("cancelled start left a releasable reservation")
	}
	if _, err := os.Stat(d.nodeCheckpointFencePath(task.RunID)); err != nil {
		t.Fatalf("cancelled start lost durable fence: %v", err)
	}
}

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
	"testing"

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
	request.Operation = domain.RunnerWorkspaceOperationNodeCheckpointCreate
	request.CheckpointLease = task.NodeCheckpointLease
	request.Repositories = []domain.RepositoryContext{{}}
	response := d.readRunnerWorkspace(context.Background(), request)
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

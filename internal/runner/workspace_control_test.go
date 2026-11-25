package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"github.com/gorilla/websocket"
)

func TestWorkspacePreparationDoesNotBlockTaskControl(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	request.Workflow.Nodes[0].Script = "touch preparation-started; sleep 2"
	d.controls = map[string]chan string{}
	d.pendingControls = map[string]string{}
	control := d.registerTaskControl("active-task")
	defer d.unregisterTaskControl("active-task")
	connections := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			connections <- conn
		}
	}))
	defer server.Close()
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-connections
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- d.readControls(ctx, client) }()
	if err := peer.WriteJSON(controlMessage{Type: "workspace.request", RequestID: "slow-preparation", WorkspaceRequest: &request}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(root, "preparation-started")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(root, "preparation-started")); err != nil {
		t.Fatal("preparation never started", err)
	}
	if err := peer.WriteJSON(controlMessage{Type: "task.control", TaskID: "active-task", DesiredState: domain.RunnerTaskDesiredCancel}); err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-control:
		if state != domain.RunnerTaskDesiredCancel {
			t.Fatalf("control = %q", state)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("long workspace preparation blocked task cancellation")
	}
	// A status read remains available while scripts run; responses share one
	// serialized writer, rather than concurrent gorilla websocket writes.
	request.Operation = domain.RunnerWorkspaceOperationPreparationStatus
	if err := peer.WriteJSON(controlMessage{Type: "workspace.request", RequestID: "status", WorkspaceRequest: &request}); err != nil {
		t.Fatal(err)
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	var response controlMessage
	if err := peer.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.RequestID != "status" || response.WorkspaceResponse == nil || response.WorkspaceResponse.Status != 200 {
		t.Fatalf("status response: %+v", response)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(4 * time.Second):
		t.Fatal("control shutdown did not join preparation")
	}
	if _, active := d.workspacePreparing.Load(request.RunID); active {
		t.Fatal("control shutdown returned before preparation stopped")
	}
	gate := d.workspaceGate(request.RunID)
	if !gate.TryLock() {
		t.Fatal("control shutdown retained workspace preparation fence")
	}
	gate.Unlock()
	d.closeAllRunSandboxes(context.Background())
}

func TestCheckpointCancellationReleasesRunnerWorkspaceForNextTurn(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "runs", "slow-export", "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(workspace, "slow.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(512 << 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	request := domain.RunnerWorkspaceRequest{RunID: "slow-export", RunStatus: "succeeded", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, Operation: domain.RunnerWorkspaceOperationCheckpointExport}
	result := make(chan error, 1)
	conn := controlTestServer(t, func(peer *websocket.Conn) {
		if err := peer.WriteJSON(controlMessage{Type: "workspace.request", RequestID: "export", WorkspaceRequest: &request}); err != nil {
			result <- err
			return
		}
		gate := d.requestGate(request)
		deadline := time.Now().Add(2 * time.Second)
		for gate.TryLock() {
			gate.Unlock()
			if time.Now().After(deadline) {
				result <- fmt.Errorf("export never acquired workspace fence")
				return
			}
			time.Sleep(time.Millisecond)
		}
		if err := peer.WriteJSON(controlMessage{Type: "workspace.cancel", RequestID: "export"}); err != nil {
			result <- err
			return
		}
		_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
		var response controlMessage
		if err := peer.ReadJSON(&response); err != nil {
			result <- err
			return
		}
		if response.WorkspaceResponse == nil || !strings.Contains(response.WorkspaceResponse.Error, "context canceled") {
			result <- fmt.Errorf("export did not stop: %+v", response)
			return
		}
		if !gate.TryLock() {
			result <- fmt.Errorf("stopped export retained workspace fence")
			return
		}
		gate.Unlock()
		request.Operation = domain.RunnerWorkspaceOperationTree
		if err := peer.WriteJSON(controlMessage{Type: "workspace.request", RequestID: "next-turn", WorkspaceRequest: &request}); err != nil {
			result <- err
			return
		}
		if err := peer.ReadJSON(&response); err != nil {
			result <- err
			return
		}
		if response.RequestID != "next-turn" || response.WorkspaceResponse == nil || response.WorkspaceResponse.Status != 200 {
			result <- fmt.Errorf("next turn blocked: %+v", response)
			return
		}
		result <- nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- d.readControls(ctx, conn) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("remote checkpoint cancellation timed out")
	}
	cancel()
	<-finished
}

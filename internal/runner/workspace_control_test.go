package runner

import (
	"context"
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

func TestWorkspaceOpeningStreamsProgressAndCancelsOnlyItsRequest(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	request.RunKind = domain.RunKindWorkspace
	request.Workflow.Nodes[0].Script = "touch opening-started; sleep 30"
	defer d.closeAllRunSandboxes(context.Background())
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
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(4 * time.Second):
			t.Error("control loop did not stop")
		}
	}()
	if err := peer.WriteJSON(controlMessage{Type: "workspace.request", RequestID: "opening", WorkspaceRequest: &request}); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	seen := false
	for !seen {
		var message controlMessage
		if err := peer.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if message.Type == "workspace.progress" && message.WorkspaceProgress != nil && message.WorkspaceProgress.Phase == "prepare" {
			seen = true
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(root, "opening-started")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(root, "opening-started")); err != nil {
		t.Fatal("preparation did not start", err)
	}
	if err := peer.WriteJSON(controlMessage{Type: "workspace.cancel", RequestID: "opening"}); err != nil {
		t.Fatal(err)
	}
	for {
		var message controlMessage
		if err := peer.ReadJSON(&message); err != nil {
			t.Fatal("cancellation did not finish request", err)
		}
		if message.Type == "workspace.response" {
			if message.RequestID != "opening" || message.WorkspaceResponse == nil || message.WorkspaceResponse.Error == "" {
				t.Fatalf("unexpected response: %+v", message)
			}
			break
		}
	}
	if _, active := d.workspacePreparing.Load(request.RunID); active {
		t.Fatal("canceled request retained preparation lock")
	}
	request.Operation = domain.RunnerWorkspaceOperationPreparationStatus
	if err := peer.WriteJSON(controlMessage{Type: "workspace.request", RequestID: "status-after-cancel", WorkspaceRequest: &request}); err != nil {
		t.Fatal(err)
	}
	var response controlMessage
	if err := peer.ReadJSON(&response); err != nil || response.RequestID != "status-after-cancel" {
		t.Fatalf("cancel closed shared connection: %+v %v", response, err)
	}
}

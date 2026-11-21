package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
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
	// Tool traffic shares the socket with live opening progress. Leave a
	// tool call pending while cancelling preparation; it must survive.
	d.conversationMu.Lock()
	forward := d.conversationForward
	d.conversationMu.Unlock()
	if forward == nil {
		t.Fatal("conversation forwarder is unavailable")
	}
	toolCtx, stopTool := context.WithTimeout(ctx, 5*time.Second)
	defer stopTool()
	toolResult := make(chan error, 1)
	go func() {
		body, err := forward(toolCtx, "fixture-token", []byte(`{"method":"ping"}`))
		if err == nil && string(body) != `{"result":{}}` {
			err = fmt.Errorf("unexpected tool response: %s", body)
		}
		toolResult <- err
	}()
	var tool controlMessage
	for tool.Type != "agent_tool.request" {
		if err := peer.ReadJSON(&tool); err != nil {
			t.Fatal("tool request blocked by workspace preparation", err)
		}
	}
	if tool.ToolRequest == nil || tool.ToolRequest.Token != "fixture-token" || string(tool.ToolRequest.Body) != `{"method":"ping"}` {
		t.Fatalf("incorrect forwarded request: %+v", tool)
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
	if err := peer.WriteJSON(controlMessage{Type: "agent_tool.response", RequestID: tool.RequestID, ToolResponse: &domain.RunnerToolResponse{Body: []byte(`{"result":{}}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := <-toolResult; err != nil {
		t.Fatal("workspace cancellation interrupted the conversation gateway", err)
	}
}

func TestWorkspaceControlConversationProcessOutlivesStartRequest(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable")
	}
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir(), ExtraBinaries: map[string]string{"fixture-cli": shell}}}
	peers := make(chan *websocket.Conn, 1)
	releasePeer := make(chan struct{})
	conn := controlTestServer(t, func(peer *websocket.Conn) { peers <- peer; <-releasePeer })
	defer close(releasePeer)
	peer := <-peers
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	finished := make(chan error, 1)
	go func() { finished <- d.readControls(ctx, conn) }()
	defer func() { cancel(); <-finished }()
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	call := func(id string, req runtime.ConversationProcessRequest) runtime.ConversationProcessResult {
		t.Helper()
		req.SessionKey = strings.Repeat("c", 64)
		request := domain.RunnerWorkspaceRequest{Operation: "conversation_process", Conversation: conversationRequest(t, req)}
		if err := peer.WriteJSON(controlMessage{Type: "workspace.request", RequestID: id, WorkspaceRequest: &request}); err != nil {
			t.Fatal(err)
		}
		var response controlMessage
		if err := peer.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if response.RequestID != id || response.WorkspaceResponse == nil || response.WorkspaceResponse.Status != 200 {
			t.Fatalf("conversation RPC failed: %+v", response)
		}
		var result runtime.ConversationProcessResult
		if err := json.Unmarshal(response.WorkspaceResponse.Payload, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	started := call("start", runtime.ConversationProcessRequest{Operation: "start", Command: &runtime.ConversationCommand{
		Provider: "fixture-cli", Interactive: true,
		Argv: []string{"fixture-cli", "-c", `read line; printf 'survived:%s' "$line"`},
	}})
	call("write", runtime.ConversationProcessRequest{Operation: "write", ID: started.ID, Data: "next RPC\n"})
	var output strings.Builder
	var after int64
	for {
		result := call("poll", runtime.ConversationProcessRequest{Operation: "poll", ID: started.ID, After: after})
		for _, event := range result.Events {
			output.WriteString(event.Message)
		}
		after = result.Sequence
		if result.Done {
			if result.Error != "" || !strings.Contains(output.String(), "survived:next RPC") {
				t.Fatalf("start RPC cancelled persistent process: error=%s output=%s", result.Error, output.String())
			}
			break
		}
	}
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

// workspace.cancel only reaches requests that own their context. A
// run-preparation request (RunKind other than workspace) runs on the daemon
// context, so a stray cancel for its ID must not abort the preparation.
func TestWorkspaceCancelDoesNotAbortRunPreparation(t *testing.T) {
	d, request, root := runnerPreparationFixture(t)
	request.Workflow.Nodes[0].Script = "touch preparation-started; sleep 1; touch preparation-finished"
	defer d.closeAllRunSandboxes(context.Background())
	peers := make(chan *websocket.Conn, 1)
	releasePeer := make(chan struct{})
	conn := controlTestServer(t, func(peer *websocket.Conn) { peers <- peer; <-releasePeer })
	defer close(releasePeer)
	peer := <-peers
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	finished := make(chan error, 1)
	go func() { finished <- d.readControls(ctx, conn) }()
	defer func() { cancel(); <-finished }()
	if err := peer.WriteJSON(controlMessage{Type: "workspace.request", RequestID: "run-prepare", WorkspaceRequest: &request}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "preparation-started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preparation did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := peer.WriteJSON(controlMessage{Type: "workspace.cancel", RequestID: "run-prepare"}); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(8 * time.Second))
	for {
		var message controlMessage
		if err := peer.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
		if message.Type == "workspace.progress" {
			t.Fatalf("run preparation streamed workspace-opening progress: %+v", message)
		}
		if message.Type != "workspace.response" {
			continue
		}
		if message.RequestID != "run-prepare" || message.WorkspaceResponse == nil || message.WorkspaceResponse.Status != 200 {
			t.Fatalf("run preparation was aborted by workspace.cancel: %+v", message.WorkspaceResponse)
		}
		break
	}
	if _, err := os.Stat(filepath.Join(root, "preparation-finished")); err != nil {
		t.Fatal("preparation script did not run to completion", err)
	}
}

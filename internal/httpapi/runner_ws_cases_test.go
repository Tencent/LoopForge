package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func TestRunnerWebsocketControlAndWorkspace(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	runnerID, session := registerTestRunner(t, f.h)
	hub := newRunnerControlHub()
	progress := make(chan domain.WorkspaceOpening, 1)
	hub.onWorkspaceProgress = func(_ string, p domain.WorkspaceOpening) { progress <- p }
	handler := &runnersHandlers{deps: &deps{repo: f.s, engine: f.e}, runnerControls: hub}
	router := gin.New()
	router.GET("/runners/ws", handler.runnerControlConnect)
	server := httptest.NewServer(router)
	defer server.Close()

	header := http.Header{"Authorization": []string{"Bearer " + session}}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/runners/ws"
	client, response, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		if response != nil {
			t.Fatalf("dial status %d: %v", response.StatusCode, err)
		}
		t.Fatal(err)
	}
	defer client.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		hub.mu.Lock()
		connected := hub.conns[runnerID] != nil
		hub.mu.Unlock()
		if connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner websocket was not registered")
		}
		time.Sleep(time.Millisecond)
	}

	type cvBWorkspaceResult struct {
		response domain.RunnerWorkspaceResponse
		err      error
	}
	result := make(chan cvBWorkspaceResult, 1)
	go func() {
		response, err := hub.requestWorkspace(context.Background(), runnerID, domain.RunnerWorkspaceRequest{
			RunID: "cvb-ws-run", RunKind: domain.RunKindWorkspace, Operation: domain.RunnerWorkspaceOperationPrepare,
		})
		result <- cvBWorkspaceResult{response: response, err: err}
	}()
	var request runnerControlMessage
	if err := client.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	if request.Type != "workspace.request" || request.RequestID == "" {
		t.Fatalf("workspace request: %+v", request)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := client.WriteJSON(runnerControlMessage{Type: "workspace.progress", RequestID: request.RequestID,
		WorkspaceProgress: &domain.WorkspaceOpening{State: "running", Phase: "clone", Output: "50 KiB/s"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-progress:
		if p.Output != "50 KiB/s" {
			t.Fatalf("incorrect progress: %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("websocket dropped workspace progress")
	}
	// A tool request must be serviced while preparation is still pending,
	// and the session must remain bound to this authenticated runner.
	for _, executionRunner := range []string{runnerID, "another-runner"} {
		session := &AgentToolSession{Mode: AgentToolAssistant, ExecutionRunnerID: executionRunner, ExpiresAt: time.Now().Add(time.Minute)}
		if err := agentToolSessions.create(session); err != nil {
			t.Fatal(err)
		}
		defer agentToolSessions.delete(session.Token)
		if err := client.WriteJSON(runnerControlMessage{Type: "agent_tool.request", RequestID: "tool-ping", ToolRequest: &domain.RunnerToolRequest{
			Token: session.Token, Body: []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`),
		}}); err != nil {
			t.Fatal(err)
		}
		var tool runnerControlMessage
		if err := client.ReadJSON(&tool); err != nil {
			t.Fatal(err)
		}
		if tool.Type != "agent_tool.response" || tool.RequestID != "tool-ping" || tool.ToolResponse == nil {
			t.Fatalf("tool response: %+v", tool)
		}
		if executionRunner != runnerID {
			if tool.ToolResponse.Error != "invalid conversation gateway" {
				t.Fatal("accepted a session belonging to another runner")
			}
		} else {
			var rpc agentToolRPCResponse
			if err := json.Unmarshal(tool.ToolResponse.Body, &rpc); err != nil || tool.ToolResponse.Error != "" || rpc.Error != nil || rpc.Result == nil {
				t.Fatalf("tool call failed during preparation: %+v %v", tool.ToolResponse, err)
			}
		}
	}
	if err := client.WriteJSON(runnerControlMessage{
		Type: "workspace.response", RequestID: request.RequestID,
		WorkspaceResponse: &domain.RunnerWorkspaceResponse{Status: http.StatusOK},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.response.Status != http.StatusOK {
			t.Fatalf("workspace result: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workspace response timed out")
	}

	task := domain.RunnerTask{ID: "cvb-ws-task", RunnerID: runnerID, LeaseOwner: runnerID, Provider: "mock",
		Prompt: "x", Status: domain.RunnerTaskRunning, DesiredState: domain.RunnerTaskDesiredCancel}
	if err := f.s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if err := client.WriteJSON(runnerControlMessage{Type: "ignored"}); err != nil {
		t.Fatal(err)
	}
	if err := client.WriteJSON(runnerControlMessage{Type: "hello", ActiveTaskIDs: []string{"missing", task.ID}}); err != nil {
		t.Fatal(err)
	}
	var control runnerControlMessage
	if err := client.ReadJSON(&control); err != nil {
		t.Fatal(err)
	}
	if control.Type != "task.control" || control.TaskID != task.ID {
		t.Fatalf("control: %+v", control)
	}

	// A response for an unknown request is deliberately ignored.
	if err := client.WriteJSON(runnerControlMessage{Type: "workspace.response", RequestID: "missing",
		WorkspaceResponse: &domain.RunnerWorkspaceResponse{Status: 404}}); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}

func TestRunnerControlHubErrorAndBufferBranches(t *testing.T) {
	hub := newRunnerControlHub()
	if _, err := hub.requestWorkspace(context.Background(), "missing", domain.RunnerWorkspaceRequest{}); err != errRunnerWorkspaceUnavailable {
		t.Fatalf("missing runner error = %v", err)
	}

	closed := &runnerControlConn{runnerID: "closed", send: make(chan runnerControlMessage), done: make(chan struct{})}
	close(closed.done)
	hub.conns[closed.runnerID] = closed
	if _, err := hub.requestWorkspace(context.Background(), closed.runnerID, domain.RunnerWorkspaceRequest{}); err != errRunnerWorkspaceUnavailable {
		t.Fatalf("closed runner error = %v", err)
	}

	cancelled := &runnerControlConn{runnerID: "cancelled", send: make(chan runnerControlMessage), done: make(chan struct{})}
	hub.conns[cancelled.runnerID] = cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hub.requestWorkspace(ctx, cancelled.runnerID, domain.RunnerWorkspaceRequest{}); err == nil {
		t.Fatal("cancelled request unexpectedly succeeded")
	}

	waiting := &runnerControlConn{runnerID: "waiting", send: make(chan runnerControlMessage, 1), done: make(chan struct{})}
	hub.conns[waiting.runnerID] = waiting
	ctx, cancel = context.WithCancel(context.Background())
	waitResult := make(chan error, 1)
	go func() {
		_, err := hub.requestWorkspace(ctx, waiting.runnerID, domain.RunnerWorkspaceRequest{})
		waitResult <- err
	}()
	select {
	case <-waiting.send:
	case <-time.After(time.Second):
		t.Fatal("request was not sent")
	}
	cancel()
	if err := <-waitResult; err == nil {
		t.Fatal("waiting request did not observe cancellation")
	}

	buffered := &runnerControlConn{runnerID: "buffered", send: make(chan runnerControlMessage, 1), done: make(chan struct{})}
	buffered.send <- runnerControlMessage{Type: "full"}
	hub.conns[buffered.runnerID] = buffered
	hub.sendControl("", "task", "cancel")
	hub.sendControl(buffered.runnerID, "", "cancel")
	hub.sendControl("missing", "task", "cancel")
	hub.sendControl(buffered.runnerID, "task", "cancel")
	close(buffered.done)

	hub.resolveWorkspace("missing", domain.RunnerWorkspaceResponse{})
}

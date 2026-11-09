package httpapi

import (
	"context"
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
			RunID: "cvb-ws-run", Operation: domain.RunnerWorkspaceOperationTree,
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

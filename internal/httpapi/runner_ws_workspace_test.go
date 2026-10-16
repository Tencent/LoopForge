package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRunnerWorkspaceRPCIsTransient(t *testing.T) {
	hub := newRunnerControlHub()
	conn := &runnerControlConn{runnerID: "runner-a", send: make(chan runnerControlMessage, 1), done: make(chan struct{})}
	hub.conns[conn.runnerID] = conn

	type result struct {
		response domain.RunnerWorkspaceResponse
		err      error
	}
	completed := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		response, err := hub.requestWorkspace(ctx, conn.runnerID, domain.RunnerWorkspaceRequest{RunID: "run-a", Operation: domain.RunnerWorkspaceOperationTree})
		completed <- result{response: response, err: err}
	}()

	message := <-conn.send
	if message.Type != "workspace.request" || message.RequestID == "" || message.WorkspaceRequest == nil {
		t.Fatalf("unexpected workspace request: %+v", message)
	}
	payload := json.RawMessage(`{"entries":[],"truncated":false}`)
	hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Payload: payload, Status: 200})
	got := <-completed
	if got.err != nil || string(got.response.Payload) != string(payload) {
		t.Fatalf("unexpected response: %+v err=%v", got.response, got.err)
	}
	if len(hub.pending) != 0 {
		t.Fatalf("completed transient requests retained in memory: %d", len(hub.pending))
	}
}

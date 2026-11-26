package managedagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestA2AEndpointPolicy(t *testing.T) {
	for _, item := range []A2AConfig{
		{Endpoint: "http://example.test/a2a"},
		{Endpoint: "https://example.test/a2a", Loopback: true},
		{Endpoint: "http://user@127.0.0.1/a2a", Loopback: true},
	} {
		if _, err := NewA2A(item, nil); err == nil {
			t.Errorf("accepted endpoint outside the configured transport policy")
		}
	}
}

func TestA2ATaskCancellation(t *testing.T) {
	polling, cancelled := make(chan struct{}, 1), make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			Params struct {
				ID string `json:"id"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		state := "working"
		switch request.Method {
		case "message/send":
		case "tasks/get":
			if request.Params.ID != "task-test" {
				t.Error("polled another task")
			}
			select {
			case polling <- struct{}{}:
			default:
			}
		case "tasks/cancel":
			if request.Params.ID != "task-test" {
				t.Error("cancelled another task")
			}
			cancelled <- struct{}{}
			state = "canceled"
		default:
			t.Errorf("unexpected method %s", request.Method)
		}
		result := map[string]any{"id": "task-test", "kind": "task", "status": map[string]string{"state": state}}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer server.Close()
	client, err := NewA2A(A2AConfig{Endpoint: server.URL, Loopback: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		select {
		case <-polling:
			cancel()
		case <-ctx.Done():
		}
	}()
	if _, err := client.Run(ctx, "test cancellation", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("the remote task was not cancelled")
	}
}

func TestA2ATurnTimeoutIsNamedAndCancelsTheTask(t *testing.T) {
	previous := turnTimeout
	turnTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { turnTimeout = previous })
	cancelled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "tasks/cancel" {
			cancelled <- struct{}{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
			"kind": "task", "id": "slow-task", "status": map[string]string{"state": "working"},
		}})
	}))
	defer server.Close()
	agent, err := NewA2A(A2AConfig{Endpoint: server.URL, Loopback: true}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.Run(context.Background(), "slow", nil)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrTurnTimedOut) || !strings.Contains(err.Error(), "A2A Agent 未在 1.5s 内完成本轮") {
		t.Fatalf("Run() error = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed-out task was not cancelled")
	}

	// A caller cancellation (run cancelled) keeps the plain context error.
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	if _, err := agent.Run(ctx, "slow", nil); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "未在") {
		t.Fatalf("cancelled Run() error = %v", err)
	}
}

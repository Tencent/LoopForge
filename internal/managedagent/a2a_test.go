package managedagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

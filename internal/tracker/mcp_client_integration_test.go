package tracker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestMCPClientHandshakeAuthAndToolCall(t *testing.T) {
	var initialized atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock","version":"1"}}}`, req.ID)
		case "notifications/initialized":
			initialized.Store(true)
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if !initialized.Load() {
				t.Fatal("tool called before initialized notification")
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"{\"items\":[{\"id\":\"1\"}]}"}]}}`, req.ID)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer gateway.Close()

	client := NewMCPClient(gateway.URL, map[string]string{"Authorization": "Bearer test-token"})
	got, err := client.CallTool("search_project_issues", map[string]any{"project_id": 7})
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"items":[{"id":"1"}]}` {
		t.Fatalf("result = %q", got)
	}
}

func TestMCPClientReadsSSEResponseAndToolErrors(t *testing.T) {
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if req.Method == "initialize" {
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"protocolVersion\":\"2024-11-05\"}}\n\n", req.ID)
			return
		}
		if calls.Add(1) == 1 {
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"boom\"}],\"isError\":true}}\n\n", req.ID)
			return
		}
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"error\":{\"code\":-32000,\"message\":\"gateway failed\"}}\n\n", req.ID)
	}))
	defer gateway.Close()

	client := NewMCPClient(gateway.URL, nil)
	if _, err := client.CallTool("broken", nil); err == nil || err.Error() != "mcp tool broken: boom" {
		t.Fatalf("tool error = %v", err)
	}
	if _, err := client.CallTool("broken-rpc", nil); err == nil || err.Error() != "mcp tool broken-rpc error [-32000]: gateway failed" {
		t.Fatalf("rpc error = %v", err)
	}
}

package mcpclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestStatefulMCPInitialization(t *testing.T) {
	var notified atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		if request.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "synthetic-session")
			writeJSON(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-03-26"}}`, request.ID))
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "synthetic-session" ||
			r.Header.Get("MCP-Protocol-Version") != "2025-03-26" {
			http.Error(w, "session or protocol header missing", http.StatusBadRequest)
			return
		}
		if request.Method == "notifications/initialized" {
			if len(request.ID) != 0 {
				http.Error(w, "notification must omit id", http.StatusBadRequest)
				return
			}
			notified.Store(true)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if !notified.Load() {
			http.Error(w, "no initialized notification", http.StatusBadRequest)
			return
		}
		if request.Method == "tools/list" {
			writeJSON(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"fixture"}]}}`, request.ID))
			return
		}
		writeJSON(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"done"}]}}`, request.ID))
	}))
	defer endpoint.Close()
	client := New(endpoint.URL, nil)
	tools, err := client.ListTools()
	if err != nil || len(tools) != 1 || tools[0].Name != "fixture" {
		t.Fatalf("stateful list failed: %v, %v", tools, err)
	}
	if text, err := client.CallTool("fixture", nil); err != nil || text != "done" {
		t.Fatalf("stateful call failed: %q, %v", text, err)
	}
}

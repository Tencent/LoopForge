package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordingGateway answers every JSON-RPC method and records the order in
// which methods reached the server.
func recordingGateway(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		methods = append(methods, req.Method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"echo"}]}}`))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), methods...)
	}
}

// The caller's context must be attached to tools/call and tools/list, while
// the initialize handshake keeps using its own background context so a
// cancelled caller does not leave the session half-initialized.
func TestRPCRequestsHonorCallerContextButInitDoesNot(t *testing.T) {
	srv, methods := recordingGateway(t)
	c := New(srv.URL, nil)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.CallToolContext(cancelled, "echo", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("CallToolContext(cancelled) err = %v, want context.Canceled", err)
	}
	if _, err := c.ListToolsContext(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListToolsContext(cancelled) err = %v, want context.Canceled", err)
	}
	got := strings.Join(methods(), ",")
	if got != "initialize,notifications/initialized" {
		t.Fatalf("methods reaching gateway = %q, want only the init handshake", got)
	}

	// nil ctx on the call path falls back to Background and succeeds.
	if out, err := c.CallToolContext(nil, "echo", nil); err != nil || out != "ok" { //nolint:staticcheck // nil ctx must fall back to Background
		t.Fatalf("CallToolContext(nil) = %q, %v", out, err)
	}
	tools, err := c.ListToolsContext(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListToolsContext = %+v, %v", tools, err)
	}
	got = strings.Join(methods(), ",")
	if got != "initialize,notifications/initialized,tools/call,tools/list" {
		t.Fatalf("methods = %q, init must run exactly once", got)
	}
}

// A transport failure during initialize is non-fatal: the client marks itself
// initialized and the real request reports the transport error instead.
func TestInitTransportFailureIsNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	c := New(url, nil)
	_, err := c.CallToolContext(context.Background(), "echo", nil)
	if err == nil || strings.Contains(err.Error(), "mcp init") || !strings.Contains(err.Error(), "mcp request echo") {
		t.Fatalf("CallToolContext err = %v, want transport error from tools/call", err)
	}
	if !c.init {
		t.Fatal("ensureInit must mark the client initialized after a transport failure")
	}
	if _, err := c.ListToolsContext(context.Background()); err == nil || !strings.Contains(err.Error(), "mcp tools/list") {
		t.Fatalf("ListToolsContext err = %v, want transport error from tools/list", err)
	}
}

package mcpclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestEveryRPCRequestCarriesStandardAndCustomHeaders(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		seen[r.Method+" "+req.Method] = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL, map[string]string{"Authorization": "Bearer tok", "X-Project": "p1"})
	if _, err := c.ListToolsContext(nil); err != nil { //nolint:staticcheck // nil ctx must fall back to Background
		t.Fatalf("ListToolsContext(nil): %v", err)
	}
	if got, err := c.CallToolContext(context.Background(), "echo", nil); err != nil || got != "ok" {
		t.Fatalf("CallToolContext = %q, %v", got, err)
	}

	for _, method := range []string{"initialize", "notifications/initialized", "tools/list", "tools/call"} {
		h, ok := seen["POST "+method]
		if !ok {
			t.Fatalf("no POST request recorded for %s; seen=%v", method, keys(seen))
		}
		for name, want := range map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "application/json, text/event-stream",
			"Authorization": "Bearer tok",
			"X-Project":     "p1",
		} {
			if got := h.Get(name); got != want {
				t.Errorf("%s header %s = %q, want %q", method, name, got, want)
			}
		}
	}
}

func TestNewRPCRequestRejectsInvalidURL(t *testing.T) {
	c := New("://bad", nil)
	if _, err := c.newRPCRequest(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("expected error for invalid URL")
	}
	// ensureInit must not panic when the notification request cannot be built.
	if err := c.ensureInit(); err == nil {
		t.Fatal("expected ensureInit to surface the invalid URL")
	}
}

func keys(m map[string]http.Header) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

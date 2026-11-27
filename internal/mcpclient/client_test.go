package mcpclient

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListToolsPreservesSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "tools/list" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"stories_get","description":"get","inputSchema":{"type":"object","required":["id"]},"annotations":{"readOnlyHint":true}}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(srv.Close)
	tools, err := New(srv.URL, nil).WithTransport(srv.Client().Transport).ListTools()
	if err != nil || len(tools) != 1 {
		t.Fatalf("list: %v %+v", err, tools)
	}
	if !bytes.Contains(tools[0].InputSchema, []byte(`"required"`)) || !bytes.Contains(tools[0].Annotations, []byte("readOnlyHint")) {
		t.Fatalf("schema not preserved: %+v", tools[0])
	}
}

func TestClonePreservesTransport(t *testing.T) {
	rt := &http.Transport{}
	c := New("http://example.invalid/mcp", map[string]string{"Authorization": "Bearer tok"}).WithTransport(rt)
	clone := c.Clone(5 * time.Second)
	if clone == nil || clone.http.Transport != http.RoundTripper(rt) {
		t.Fatalf("clone dropped the explicit transport: %+v", clone)
	}
	if clone.Header("Authorization") != "Bearer tok" {
		t.Fatalf("clone headers = %q", clone.Header("Authorization"))
	}
	if c.Clone(0).http.Transport != http.RoundTripper(rt) {
		t.Fatalf("clone without timeout dropped the explicit transport")
	}
}

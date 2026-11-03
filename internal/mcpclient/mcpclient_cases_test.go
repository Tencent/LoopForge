package mcpclient

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMCPClientHelpersAndSSE(t *testing.T) {
	var nilClient *Client
	if nilClient.Header("x") != "" || nilClient.Clone(time.Second) != nil {
		t.Fatal("nil client helpers")
	}
	c := New("http://example.invalid/", map[string]string{"Authorization": "x"})
	if c.Header("authorization") != "x" || c.Clone(time.Second).Header("Authorization") != "x" {
		t.Fatal("headers")
	}
	c.SetHeaders(map[string]string{"X-Test": "y"})
	if c.Header("x-test") != "y" {
		t.Fatal("set headers")
	}
	if c.Header("missing") != "" || cloneHeaders(nil) != nil {
		t.Fatal("missing/nil headers")
	}
	body := strings.NewReader("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":0}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\ndata: \"result\":{\"ok\":true}}\n\n")
	raw, err := c.readSSE(body)
	if err != nil || !bytes.Contains(raw, []byte(`"ok":true`)) {
		t.Fatalf("SSE = %s, %v", raw, err)
	}
	if _, err := c.readSSE(strings.NewReader("event: ping\n\n")); err == nil {
		t.Fatal("empty SSE accepted")
	}
	if raw, err := c.readSSE(strings.NewReader(`data: {"jsonrpc":"2.0","id":1,"result":{}}`)); err != nil || len(raw) == 0 {
		t.Fatalf("trailing SSE = %s, %v", raw, err)
	}
	resp := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"x":1}`))}
	if raw, err := c.readResponse(resp); err != nil || string(raw) != `{"x":1}` {
		t.Fatalf("response = %s, %v", raw, err)
	}
}

func TestMCPClientListAndCallErrorResponses(t *testing.T) {
	responses := []struct {
		status int
		body   string
	}{
		{500, "down"},
		{200, "bad"},
		{200, `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"no"}}`},
		{200, `{"jsonrpc":"2.0","id":1,"result":"bad"}`},
	}
	for _, tc := range responses {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		c := New(server.URL, nil)
		c.init = true
		if _, err := c.ListToolsContext(nil); err == nil {
			t.Fatalf("list accepted %d %q", tc.status, tc.body)
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":"gateway failed"}`))
	}))
	c := New(server.URL, nil)
	c.init = true
	if _, err := c.CallToolContext(nil, "x", nil); err == nil {
		t.Fatal("gateway error accepted")
	}
	server.Close()
}

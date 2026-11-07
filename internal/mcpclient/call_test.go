package mcpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeMCP answers initialize/notifications itself and delegates tools/call
// to respond, recording the methods and auth headers it saw.
type fakeMCP struct {
	respond func(w http.ResponseWriter, id int64)
	inits   atomic.Int32
	auth    atomic.Value
}

func (f *fakeMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &req)
	f.auth.Store(r.Header.Get("Authorization"))
	switch req.Method {
	case "initialize":
		f.inits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	default:
		f.respond(w, req.ID)
	}
}

func newFake(t *testing.T, respond func(w http.ResponseWriter, id int64)) (*fakeMCP, *Client) {
	t.Helper()
	fake := &fakeMCP{respond: respond}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, New(srv.URL, map[string]string{"Authorization": "Bearer tok"})
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestCallToolJoinsTextBlocksAndInitializesOnce(t *testing.T) {
	fake, client := newFake(t, func(w http.ResponseWriter, id int64) {
		writeJSON(w, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"a"},{"type":"image","text":"x"},{"type":"text","text":"b"}]}}`)
	})
	for i := 0; i < 2; i++ {
		out, err := client.CallTool("search", map[string]any{"q": "x"})
		if err != nil || out != "a\nb" {
			t.Fatalf("CallTool = %q, %v", out, err)
		}
	}
	if fake.inits.Load() != 1 {
		t.Fatalf("initialize should run once, ran %d times", fake.inits.Load())
	}
	if fake.auth.Load() != "Bearer tok" {
		t.Fatalf("headers should be forwarded, got %v", fake.auth.Load())
	}
}

func TestCallToolParsesSSE(t *testing.T) {
	_, client := newFake(t, func(w http.ResponseWriter, id int64) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
			"data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"streamed\"}]}}\n\n"))
	})
	out, err := client.CallTool("search", nil)
	if err != nil || out != "streamed" {
		t.Fatalf("SSE CallTool = %q, %v", out, err)
	}
}

func TestCallToolErrors(t *testing.T) {
	cases := map[string]struct {
		respond func(w http.ResponseWriter, id int64)
		want    string
	}{
		"tool isError": {func(w http.ResponseWriter, id int64) {
			writeJSON(w, `{"jsonrpc":"2.0","id":2,"result":{"isError":true,"content":[{"type":"text","text":"denied"}]}}`)
		}, "denied"},
		"rpc error": {func(w http.ResponseWriter, id int64) {
			writeJSON(w, `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"no such tool"}}`)
		}, "no such tool"},
		"http status": {func(w http.ResponseWriter, id int64) {
			http.Error(w, "upstream down", http.StatusBadGateway)
		}, "HTTP 502"},
		"gateway error body": {func(w http.ResponseWriter, id int64) {
			writeJSON(w, `{"error":"token expired"}`)
		}, "token expired"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, client := newFake(t, tc.respond)
			_, err := client.CallTool("search", nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestCloneKeepsHeadersIndependent(t *testing.T) {
	c := New("http://example.invalid", map[string]string{"X-A": "1"})
	clone := c.Clone(0)
	clone.SetHeaders(map[string]string{"X-A": "2"})
	if c.Header("X-A") != "1" || clone.Header("X-A") != "2" {
		t.Fatalf("clone should not share headers: %q %q", c.Header("X-A"), clone.Header("X-A"))
	}
}

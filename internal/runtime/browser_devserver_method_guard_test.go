package runtime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
)

// The dev-server proxy maps every allowed method (any case, padded) to its
// canonical name, and refuses everything else or a CR/LF path before any
// request reaches the dev server.
func TestHandleDevServerHTTPMethodAllowlistAndPathGuard(t *testing.T) {
	var hits atomic.Int32
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Seen-Method", r.Method)
	}))
	defer dev.Close()
	_, rawPort, _ := net.SplitHostPort(strings.TrimPrefix(dev.URL, "http://"))
	port, _ := strconv.Atoi(rawPort)
	lookup := devServerLookup
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) { return browserEndpoint{Kind: "local"}, true }
	t.Cleanup(func() { devServerLookup = lookup })
	devServerClients.Lock()
	delete(devServerClients.items, "dev-method-run")
	devServerClients.Unlock()
	send := func(method, path string) (domain.BrowserState, error) {
		return HandleBrowser(context.Background(), "dev-method-run", domain.BrowserRequest{Operation: "http",
			HTTP: &domain.BrowserHTTPRequest{Port: port, Method: method, Path: path}})
	}

	for raw, want := range map[string]string{
		"":          http.MethodGet,
		"get":       http.MethodGet,
		" head ":    http.MethodHead,
		"post":      http.MethodPost,
		"Put":       http.MethodPut,
		"patch":     http.MethodPatch,
		"DELETE":    http.MethodDelete,
		"\toptions": http.MethodOptions,
	} {
		state, err := send(raw, "/")
		if err != nil || state.HTTP == nil {
			t.Fatalf("method %q: state=%+v err=%v", raw, state, err)
		}
		if got := http.Header(state.HTTP.Header).Get("X-Seen-Method"); got != want {
			t.Fatalf("method %q reached dev server as %q, want %q", raw, got, want)
		}
	}

	before := hits.Load()
	for _, method := range []string{"CONNECT", "trace", "PROPFIND", "GET\r\nX-Injected: 1", "G ET"} {
		if _, err := send(method, "/"); err == nil {
			t.Fatalf("method %q accepted", method)
		}
	}
	for _, path := range []string{"/a\nb", "/a\rb", "/\r\n"} {
		if _, err := send("GET", path); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
	if after := hits.Load(); after != before {
		t.Fatalf("rejected requests reached the dev server %d times", after-before)
	}
}

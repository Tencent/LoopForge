package runtime

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type devServerCtxKey struct{}

func TestNewDevServerHTTPClientNeverUsesAProxy(t *testing.T) {
	transport := newDevServerHTTPClient(browserEndpoint{Kind: "host"}).Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("dev server traffic must dial the sandbox directly, not an environment proxy")
	}
}

func TestNewDevServerHTTPClientPassesDialContextAndKeepsEndpointsApart(t *testing.T) {
	dial := devServerDial
	t.Cleanup(func() { devServerDial = dial })
	var gotCtx context.Context
	var gotEndpoints []browserEndpoint
	devServerDial = func(ctx context.Context, e browserEndpoint, _ int) (net.Conn, error) {
		gotCtx = ctx
		gotEndpoints = append(gotEndpoints, e)
		server, peer := net.Pipe()
		t.Cleanup(func() { server.Close(); peer.Close() })
		return server, nil
	}
	first := browserEndpoint{Kind: "docker", Container: "sidecar-a"}
	second := browserEndpoint{Kind: "docker", Container: "sidecar-b"}
	a := newDevServerHTTPClient(first).Transport.(*http.Transport)
	b := newDevServerHTTPClient(second).Transport.(*http.Transport)
	if a == b {
		t.Fatal("each endpoint needs its own transport")
	}
	ctx := context.WithValue(context.Background(), devServerCtxKey{}, "request")
	if _, err := a.DialContext(ctx, "tcp", "localhost:5173"); err != nil {
		t.Fatal(err)
	}
	if gotCtx == nil || gotCtx.Value(devServerCtxKey{}) != "request" {
		t.Fatal("dial must run under the caller's context so cancellation reaches docker exec")
	}
	if _, err := b.DialContext(context.Background(), "tcp", "localhost:5173"); err != nil {
		t.Fatal(err)
	}
	if len(gotEndpoints) != 2 || gotEndpoints[0] != first || gotEndpoints[1] != second {
		t.Fatalf("dialed endpoints %+v", gotEndpoints)
	}
}

func TestNewDevServerHTTPClientReusesConnections(t *testing.T) {
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok "+r.URL.Path)
	}))
	defer dev.Close()
	dial := devServerDial
	t.Cleanup(func() { devServerDial = dial })
	var dials atomic.Int32
	devServerDial = func(ctx context.Context, _ browserEndpoint, _ int) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", strings.TrimPrefix(dev.URL, "http://"))
	}
	client := newDevServerHTTPClient(browserEndpoint{Kind: "docker", Container: "sidecar"})
	defer client.CloseIdleConnections()
	for _, path := range []string{"/a", "/b", "/c"} {
		res, err := client.Get("http://localhost:5173" + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(body) != "ok "+path {
			t.Fatalf("body %q", body)
		}
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("dials = %d, want one kept-alive connection (each docker dial is an exec)", n)
	}
}

func TestDevServerClientForCachesClientBuiltForLookedUpEndpoint(t *testing.T) {
	lookup, dial := devServerLookup, devServerDial
	const runID = "client-guard-run"
	t.Cleanup(func() {
		devServerLookup, devServerDial = lookup, dial
		devServerClients.Lock()
		if c := devServerClients.items[runID]; c != nil {
			c.client.CloseIdleConnections()
		}
		delete(devServerClients.items, runID)
		devServerClients.Unlock()
	})
	endpoint := browserEndpoint{Kind: "docker", Container: "guard-a"}
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) { return endpoint, true }
	var dialed []browserEndpoint
	devServerDial = func(_ context.Context, e browserEndpoint, _ int) (net.Conn, error) {
		dialed = append(dialed, e)
		server, peer := net.Pipe()
		t.Cleanup(func() { server.Close(); peer.Close() })
		return server, nil
	}
	dialThrough := func(c *http.Client) {
		t.Helper()
		if _, err := c.Transport.(*http.Transport).DialContext(context.Background(), "tcp", "localhost:3000"); err != nil {
			t.Fatal(err)
		}
	}

	first, got, ok := devServerClientFor(context.Background(), runID)
	if !ok || got != endpoint || first == nil {
		t.Fatalf("client=%v endpoint=%+v ok=%v", first, got, ok)
	}
	again, _, ok := devServerClientFor(context.Background(), runID)
	if !ok || again != first {
		t.Fatal("a fresh endpoint must hand back the cached client")
	}
	dialThrough(first)

	// The sidecar moved: the stale entry is rechecked and replaced by a client
	// that dials the new endpoint.
	forgetDevServerEndpoint(runID, endpoint)
	moved := browserEndpoint{Kind: "docker", Container: "guard-b"}
	devServerLookup = func(context.Context, string) (browserEndpoint, bool) { return moved, true }
	next, got, ok := devServerClientFor(context.Background(), runID)
	if !ok || got != moved || next == first {
		t.Fatalf("moved endpoint: endpoint=%+v ok=%v reused=%v", got, ok, next == first)
	}
	dialThrough(next)
	if len(dialed) != 2 || dialed[0] != endpoint || dialed[1] != moved {
		t.Fatalf("dialed %+v", dialed)
	}
}

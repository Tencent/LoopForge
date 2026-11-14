package runtime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNewDevServerHTTPClientKeepsRedirectsAndTransportSettings(t *testing.T) {
	client := newDevServerHTTPClient(browserEndpoint{Kind: "host"})
	if client.CheckRedirect == nil || !errors.Is(client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("redirects must be handed back to the browser")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	if transport.MaxIdleConnsPerHost != 8 || transport.IdleConnTimeout != 90*time.Second ||
		transport.ResponseHeaderTimeout != 2*time.Minute || transport.ForceAttemptHTTP2 {
		t.Fatalf("transport settings = %+v", transport)
	}
}

func TestNewDevServerHTTPClientDialsEndpointPort(t *testing.T) {
	dial := devServerDial
	t.Cleanup(func() { devServerDial = dial })
	endpoint := browserEndpoint{Kind: "docker", Container: "sidecar"}
	var gotEndpoint browserEndpoint
	var gotPort int
	server, peer := net.Pipe()
	t.Cleanup(func() { server.Close(); peer.Close() })
	devServerDial = func(_ context.Context, e browserEndpoint, port int) (net.Conn, error) {
		gotEndpoint, gotPort = e, port
		return server, nil
	}
	transport := newDevServerHTTPClient(endpoint).Transport.(*http.Transport)
	conn, err := transport.DialContext(context.Background(), "tcp", "localhost:5173")
	if err != nil || conn != server {
		t.Fatalf("conn=%v err=%v", conn, err)
	}
	if gotEndpoint != endpoint || gotPort != 5173 {
		t.Fatalf("dialed %+v port %d", gotEndpoint, gotPort)
	}
}

func TestNewDevServerHTTPClientWrapsDialFailure(t *testing.T) {
	dial := devServerDial
	t.Cleanup(func() { devServerDial = dial })
	cause := errors.New("connection refused")
	devServerDial = func(context.Context, browserEndpoint, int) (net.Conn, error) { return nil, cause }
	transport := newDevServerHTTPClient(browserEndpoint{Kind: "host"}).Transport.(*http.Transport)
	_, err := transport.DialContext(context.Background(), "tcp", "localhost:3000")
	var dialErr devServerDialError
	if !errors.As(err, &dialErr) || dialErr.err != cause || err.Error() != cause.Error() {
		t.Fatalf("err = %#v", err)
	}
}

func TestNewDevServerHTTPClientRejectsBadAddress(t *testing.T) {
	dial := devServerDial
	t.Cleanup(func() { devServerDial = dial })
	devServerDial = func(context.Context, browserEndpoint, int) (net.Conn, error) {
		t.Fatal("dial must not run for a bad address")
		return nil, nil
	}
	transport := newDevServerHTTPClient(browserEndpoint{Kind: "host"}).Transport.(*http.Transport)
	for _, addr := range []string{"localhost", "localhost:http"} {
		_, err := transport.DialContext(context.Background(), "tcp", addr)
		var dialErr devServerDialError
		if err == nil || errors.As(err, &dialErr) {
			t.Fatalf("%s: err = %#v", addr, err)
		}
	}
}

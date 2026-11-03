package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func controlTestServer(t *testing.T, serve func(*websocket.Conn)) *websocket.Conn {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		serve(ws)
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func shortControlReadTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := controlReadTimeout
	controlReadTimeout = d
	t.Cleanup(func() { controlReadTimeout = old })
}

// A socket that silently stops delivering anything (half-open TCP) must end
// readControls so controlLoop reconnects, instead of blocking forever.
func TestReadControlsGivesUpOnSilentConnection(t *testing.T) {
	shortControlReadTimeout(t, 150*time.Millisecond)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	conn := controlTestServer(t, func(*websocket.Conn) { <-release })
	d := &Daemon{}
	result := make(chan error, 1)
	start := time.Now()
	go func() { result <- d.readControls(context.Background(), conn) }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("readControls returned nil on a dead socket")
		}
		if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
			t.Fatalf("gave up before the read timeout: %s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readControls stayed blocked on a silent socket")
	}
}

// The server only pings an idle runner. Each ping must extend the deadline and
// still be answered with a pong, so a live idle socket is never dropped.
func TestReadControlsStaysOnlineWhileServerPings(t *testing.T) {
	shortControlReadTimeout(t, 150*time.Millisecond)
	var pongs atomic.Int32
	stop := make(chan struct{})
	conn := controlTestServer(t, func(ws *websocket.Conn) {
		ws.SetPongHandler(func(string) error { pongs.Add(1); return nil })
		go func() {
			for {
				if _, _, err := ws.ReadMessage(); err != nil {
					return
				}
			}
		}()
		ticker := time.NewTicker(40 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := ws.WriteControl(websocket.PingMessage, []byte("p"), time.Now().Add(time.Second)); err != nil {
					return
				}
			}
		}
	})
	d := &Daemon{}
	result := make(chan error, 1)
	go func() { result <- d.readControls(context.Background(), conn) }()
	select {
	case err := <-result:
		t.Fatalf("live socket dropped while the server was pinging: %v", err)
	case <-time.After(600 * time.Millisecond):
	}
	if pongs.Load() == 0 {
		t.Fatal("pings were not answered with pongs")
	}
	close(stop)
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("readControls did not end after the server went away")
	}
}

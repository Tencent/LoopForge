package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func runnerWSRegistered(hub *runnerControlHub, runnerID string) bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return hub.conns[runnerID] != nil
}

// A runner socket that goes silent (half-open TCP: no pong, no message) is
// dropped after the read timeout, so workspace requests stop being written
// into a dead connection. One that keeps answering stays registered.
func TestRunnerControlDropsSilentSocketAndKeepsLiveOne(t *testing.T) {
	old := runnerWSReadTimeout
	runnerWSReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { runnerWSReadTimeout = old })

	f := cvBNewHTTPFixture(t)
	runnerID, session := registerTestRunner(t, f.h)
	hub := newRunnerControlHub()
	handler := &runnersHandlers{deps: &deps{repo: f.s, engine: f.e}, runnerControls: hub}
	router := gin.New()
	router.GET("/runners/ws", handler.runnerControlConnect)
	server := httptest.NewServer(router)
	defer server.Close()
	dial := func() *websocket.Conn {
		t.Helper()
		header := http.Header{"Authorization": []string{"Bearer " + session}}
		client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/runners/ws", header)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !runnerWSRegistered(hub, runnerID) {
			if time.Now().After(deadline) {
				t.Fatal("runner websocket was not registered")
			}
			time.Sleep(time.Millisecond)
		}
		return client
	}

	live := dial()
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if live.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second)) != nil {
					return
				}
			}
		}
	}()
	time.Sleep(700 * time.Millisecond)
	if !runnerWSRegistered(hub, runnerID) {
		t.Fatal("a runner answering with pongs was dropped")
	}
	close(stop)

	deadline := time.Now().Add(5 * time.Second)
	for runnerWSRegistered(hub, runnerID) {
		if time.Now().After(deadline) {
			t.Fatal("a silent runner socket stayed registered past the read timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = live.Close()
}

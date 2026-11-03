package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func TestBrowserStreamPushesFramesAndRunsCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := runtime.FindChromium(); err != nil {
		t.Skip("chromium not installed")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<title>%s</title><h1>stream</h1>`, r.URL.Path)
	}))
	defer page.Close()
	a, owner, _, outsider, _ := sharedWorkbenchFixture(t)
	en := engine.New(a.repo, runtime.NewManager(t.TempDir()))
	a.deps.engine = en
	h := &browserHandlers{deps: a.deps}
	r, _ := a.repo.RunState("shared-run")
	r.ID, r.Status, r.Nodes, r.WorkspaceCleanedAt = "stream-run-"+t.Name(), "succeeded", map[string]*domain.NodeRun{}, time.Time{}
	if err := a.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(en.RunWorkspace(r.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := en.DeleteRun(r.ID); err != nil {
			t.Error(err)
		}
	}()
	router := gin.New()
	var viewer domain.User
	router.GET("/runs/:id/browser/stream", func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(viewer)))
		h.stream(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	dial := func(u domain.User) *websocket.Conn {
		t.Helper()
		viewer = u
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/runs/"+r.ID+"/browser/stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	// next returns the first state or error matching want, noting whether a JPEG frame arrived.
	next := func(ws *websocket.Conn, want func(*browserStreamMessage) bool) (browserStreamMessage, bool) {
		t.Helper()
		sawFrame := false
		_ = ws.SetReadDeadline(time.Now().Add(20 * time.Second))
		for {
			kind, raw, err := ws.ReadMessage()
			if err != nil {
				t.Fatalf("stream read: %v", err)
			}
			if kind == websocket.BinaryMessage {
				sawFrame = sawFrame || len(raw) > 2 && raw[0] == 0xFF && raw[1] == 0xD8
				continue
			}
			var msg browserStreamMessage
			if json.Unmarshal(raw, &msg) == nil && want(&msg) {
				return msg, sawFrame
			}
		}
	}

	idle := dial(owner)
	if msg, _ := next(idle, func(m *browserStreamMessage) bool { return m.Type == "state" }); msg.State.Available || msg.State.Reason == "" {
		t.Fatalf("no browser yet should report a reason: %+v", msg.State)
	}
	idle.Close()

	if _, opErr := h.dispatch(context.Background(), r, domain.BrowserRequest{Operation: "navigate", URL: page.URL + "/first", Ensure: true}); opErr != "" {
		t.Fatal(opErr)
	}
	ws := dial(owner)
	defer ws.Close()
	next(ws, func(m *browserStreamMessage) bool {
		return m.Type == "state" && m.State.Available && strings.HasSuffix(m.State.URL, "/first")
	})
	_ = ws.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		kind, raw, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("no frame pushed: %v", err)
		}
		if kind == websocket.BinaryMessage {
			if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xD8 {
				t.Fatalf("frame is not a JPEG: % x", raw[:min(len(raw), 4)])
			}
			break
		}
	}
	if err := ws.WriteJSON(domain.BrowserRequest{Operation: "navigate", URL: page.URL + "/second"}); err != nil {
		t.Fatal(err)
	}
	next(ws, func(m *browserStreamMessage) bool {
		return m.Type == "state" && strings.HasSuffix(m.State.URL, "/second")
	})
	if err := ws.WriteJSON(domain.BrowserRequest{Operation: "resize", FrameWidth: 400}); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteJSON(domain.BrowserRequest{Operation: "navigate", URL: page.URL + "/third"}); err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		kind, raw, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("frames never shrank to the panel width: %v", err)
		}
		if kind != websocket.BinaryMessage {
			continue
		}
		if config, err := jpeg.DecodeConfig(bytes.NewReader(raw)); err == nil && config.Width <= 400 {
			break
		}
	}

	watcher := dial(outsider)
	defer watcher.Close()
	if err := watcher.WriteJSON(domain.BrowserRequest{Operation: "navigate", URL: page.URL + "/outsider"}); err != nil {
		t.Fatal(err)
	}
	if msg, _ := next(watcher, func(m *browserStreamMessage) bool { return m.Type == "error" }); !strings.Contains(msg.Error, "无权") {
		t.Fatalf("outsider control: %+v", msg)
	}
}

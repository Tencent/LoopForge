package botinteraction

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gorilla/websocket"
)

func TestReceiptBeforeModelAndFinalReplacesSameStream(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "progress-bot", BotID: "external", OwnerID: 42, ProjectID: "p", Enabled: true, InteractionEnabled: true}
	if err = store.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	frames := make(chan Frame, 10)
	var upgrader websocket.Upgrader
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := upgrader.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		for {
			var f Frame
			if ws.ReadJSON(&f) != nil {
				return
			}
			_ = ws.WriteJSON(map[string]any{"headers": f.Headers, "errcode": 0})
			if f.Cmd == "aibot_subscribe" {
				_ = ws.WriteJSON(map[string]any{"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": "original-request"}, "body": map[string]any{"msgid": "message", "aibotid": "external", "chattype": "single", "from": map[string]string{"userid": "user"}, "msgtype": "text", "text": map[string]string{"content": "查询项目"}}})
			}
			if f.Cmd == "aibot_respond_msg" || f.Cmd == "aibot_send_msg" {
				frames <- f
			}
		}
	}))
	defer srv.Close()
	service := New(store)
	service.URL = "ws" + strings.TrimPrefix(srv.URL, "http")
	release := make(chan struct{})
	service.Generate = func(ctx context.Context, _ domain.NotificationBot, _ domain.BotConversation, _ []domain.BotInteraction, _ domain.BotInteraction) (string, error) {
		ReportProgress(ctx, "正在查询平台")
		select {
		case <-release:
			return "实际查询结果", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	defer func() { cancel(); <-service.Done }()
	read := func() Frame {
		select {
		case f := <-frames:
			return f
		case <-time.After(12 * time.Second):
			t.Fatal("missing stream frame")
			return Frame{}
		}
	}
	first := read()
	var initial struct {
		Stream struct {
			ID, Content string
			Finish      bool
		}
	}
	if err = json.Unmarshal(first.Body, &initial); err != nil {
		t.Fatal(err)
	}
	if first.Cmd != "aibot_respond_msg" || first.Headers.RequestID != "original-request" || initial.Stream.Finish || initial.Stream.Content != "已收到，正在处理…" {
		t.Fatalf("receipt: %+v %s", first, first.Body)
	}
	intermediate := read()
	var progress struct {
		Stream struct {
			ID, Content string
			Finish      bool
		}
	}
	_ = json.Unmarshal(intermediate.Body, &progress)
	if progress.Stream.ID != initial.Stream.ID || progress.Stream.Finish || !strings.Contains(progress.Stream.Content, "正在查询平台") {
		t.Fatalf("progress: %s", intermediate.Body)
	}
	close(release)
	final := read()
	var result struct {
		Stream struct {
			ID, Content string
			Finish      bool
		}
	}
	_ = json.Unmarshal(final.Body, &result)
	if result.Stream.ID != initial.Stream.ID || !result.Stream.Finish || result.Stream.Content != "实际查询结果" || final.Headers.RequestID != first.Headers.RequestID {
		t.Fatalf("final: %s", final.Body)
	}
	eventually(t, func() bool {
		rows, _ := store.BotInteractionRecords(bot.ID)
		return len(rows) == 1 && rows[0].Status == "replied"
	})
	select {
	case f := <-frames:
		t.Fatalf("unexpected duplicate message: %+v", f)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestCanceledProgressStillFinishesOriginalStream(t *testing.T) {
	var upgrader websocket.Upgrader
	frames := make(chan Frame, 5)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			var frame Frame
			if ws.ReadJSON(&frame) != nil {
				return
			}
			_ = ws.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 0})
			if frame.Cmd == "aibot_respond_msg" {
				frames <- frame
			}
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws, err := dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "bot", "secret", func(*socket, Frame) {})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.close()
	item := domain.BotInteraction{ID: "original-stream", RequestID: "callback"}
	if err = ws.stream(ctx, item, "正在处理", false); err != nil {
		t.Fatal(err)
	}
	expired, stop := context.WithCancel(ctx)
	stop()
	if err = ws.stream(expired, item, "正在处理", false); err == nil {
		t.Fatal("expected cancellation")
	}
	if err = ws.stream(ctx, item, "本次处理已超时", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		f := <-frames
		var body struct {
			Stream struct {
				ID      string
				Finish  bool
				Content string
			}
		}
		_ = json.Unmarshal(f.Body, &body)
		if body.Stream.ID != item.ID || (i == 1 && (!body.Stream.Finish || body.Stream.Content != "本次处理已超时")) {
			t.Fatal(string(f.Body))
		}
	}
}

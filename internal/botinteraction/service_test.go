package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func eventually(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
func TestSocketChatDiscoveryDedupeResetAndCardCallbacks(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "bot-test", BotID: "external", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true}
	if err = store.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	var conn *websocket.Conn
	var writeMu sync.Mutex
	authenticated := make(chan struct{})
	replies := make(chan map[string]any, 20)
	var upgrader = websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn = ws
		defer ws.Close()
		for {
			var frame map[string]any
			if ws.ReadJSON(&frame) != nil {
				return
			}
			cmd := frame["cmd"]
			if cmd == "aibot_subscribe" {
				body := frame["body"].(map[string]any)
				if body["secret"] != "secret" {
					t.Error("wrong secret")
				}
			}
			writeMu.Lock()
			ws.WriteJSON(map[string]any{"headers": frame["headers"], "errcode": 0})
			writeMu.Unlock()
			if cmd == "aibot_subscribe" {
				close(authenticated)
			}
			finished := false
			if cmd == "aibot_respond_msg" {
				finished, _ = frame["body"].(map[string]any)["stream"].(map[string]any)["finish"].(bool)
			}
			if finished || cmd == "aibot_send_msg" || cmd == "aibot_respond_update_msg" {
				replies <- frame
			}
		}
	}))
	defer srv.Close()
	service := New(store)
	service.URL = "ws" + strings.TrimPrefix(srv.URL, "http")
	var callsMu sync.Mutex
	var calls []struct {
		Text, Chat, Session string
		History             int
	}
	service.Generate = func(ctx context.Context, b domain.NotificationBot, c domain.BotConversation, h []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		callsMu.Lock()
		calls = append(calls, struct {
			Text, Chat, Session string
			History             int
		}{item.Content, c.ChatID, c.SessionID, len(h)})
		callsMu.Unlock()
		return "答复：" + item.Content, nil
	}
	actionCalled := make(chan struct{}, 1)
	service.Action = func(ctx context.Context, b domain.NotificationBot, item domain.BotInteraction, c domain.BotConversation) (string, error) {
		if item.Action != "approve" || c.ChatID != "group-new" {
			t.Error("wrong action scope")
		}
		actionCalled <- struct{}{}
		return "已通过", nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	defer func() { cancel(); <-service.Done }()
	select {
	case <-authenticated:
	case <-time.After(6 * time.Second):
		t.Fatal("not authenticated")
	}
	send := func(id, chat, kind, text string) {
		body := map[string]any{"msgid": id, "aibotid": "external", "chatid": chat, "chattype": kind, "from": map[string]string{"userid": "sender"}, "msgtype": "text", "text": map[string]string{"content": text}}
		writeMu.Lock()
		err := conn.WriteJSON(map[string]any{"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": id}, "body": body})
		writeMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	waitReply := func() {
		select {
		case <-replies:
		case <-time.After(6 * time.Second):
			t.Fatal("missing reply")
		}
	}
	send("m1", "", "single", "第一句")
	waitReply()
	send("m1", "", "single", "第一句") // Duplicate event must not repeat the model call.
	send("m2", "group-new", "group", "群里提问")
	waitReply()
	send("m3", "", "single", "/new")
	waitReply()
	send("m4", "", "single", "新话题")
	waitReply()
	eventually(t, func() bool { callsMu.Lock(); defer callsMu.Unlock(); return len(calls) == 3 })
	callsMu.Lock()
	if calls[0].Chat != "sender" || calls[1].Chat != "group-new" || calls[2].History != 0 || calls[0].Session == calls[2].Session {
		t.Fatalf("contexts: %+v", calls)
	}
	callsMu.Unlock()
	// Card callbacks use the same new group, and duplicates do not repeat actions.
	event := map[string]any{"msgid": "click", "aibotid": "external", "chatid": "group-new", "chattype": "group", "from": map[string]string{"userid": "other-member"}, "msgtype": "event", "event": map[string]any{"eventtype": "template_card_event", "template_card_event": map[string]string{"event_key": "approve", "task_id": "delivery"}}}
	for i := 0; i < 2; i++ {
		writeMu.Lock()
		conn.WriteJSON(map[string]any{"cmd": "aibot_event_callback", "headers": map[string]string{"req_id": "click"}, "body": event})
		writeMu.Unlock()
	}
	select {
	case <-actionCalled:
	case <-time.After(6 * time.Second):
		t.Fatal("action not processed")
	}
	eventually(t, func() bool {
		records, _ := store.BotInteractionRecords(bot.ID)
		for _, m := range records {
			if m.Action == "approve" {
				return m.Status == "replied"
			}
		}
		return false
	})
	select {
	case <-actionCalled:
		t.Fatal("duplicate action executed")
	default:
	}
}
func TestNormalizeAndCommands(t *testing.T) {
	bot := domain.NotificationBot{ID: "b", BotID: "external"}
	raw, _ := json.Marshal(map[string]any{"aibotid": "wrong", "msgid": "1", "from": map[string]string{"userid": "u"}, "chattype": "single", "msgtype": "text", "text": map[string]string{"content": "hello"}})
	if _, _, _, ok := normalizeIncoming(bot, Frame{Body: raw}); ok {
		t.Fatal("accepted foreign Bot")
	}
	for _, cmd := range []string{"/new", "开启新会话", "新会话", "@Bot /new", "@Bot\n开启新会话"} {
		if !newConversationCommand(cmd) {
			t.Fatal(cmd)
		}
	}
	if newConversationCommand("帮我新建一个会话数据表") {
		t.Fatal("natural message reset context")
	}
}

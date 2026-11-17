package botinteraction

// Characterization tests for the existing WeCom behaviour. They pin the
// identifiers and delivery semantics that the enterprise IM refactor must keep
// unchanged for stored WeCom bots (implementation plan R1-1). Do not edit these
// expectations while extracting the channel interface; changes to WeCom
// delivery semantics belong to a later round with their own acceptance.

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func baselineSHA(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

type wecomFrame struct {
	Conn  int
	Cmd   string
	ReqID string
	Body  map[string]any
}

// fakeWeCom is a scripted WeCom AI Bot long-connection endpoint. decide is
// called for every client frame and returns the ack errcode, or -1 to drop the
// connection without acknowledging.
type fakeWeCom struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	frames  []wecomFrame
	conns   int32
	current *websocket.Conn
	writeMu sync.Mutex
	ready   chan int
	decide  func(conn int, f wecomFrame) int
}

func newFakeWeCom(t *testing.T, decide func(conn int, f wecomFrame) int) *fakeWeCom {
	f := &fakeWeCom{t: t, ready: make(chan int, 8), decide: decide}
	var upgrader websocket.Upgrader
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		n := int(atomic.AddInt32(&f.conns, 1))
		f.mu.Lock()
		f.current = ws
		f.mu.Unlock()
		for {
			var raw map[string]any
			if ws.ReadJSON(&raw) != nil {
				return
			}
			frame := wecomFrame{Conn: n}
			frame.Cmd, _ = raw["cmd"].(string)
			if headers, ok := raw["headers"].(map[string]any); ok {
				frame.ReqID, _ = headers["req_id"].(string)
			}
			frame.Body, _ = raw["body"].(map[string]any)
			f.mu.Lock()
			f.frames = append(f.frames, frame)
			f.mu.Unlock()
			code := 0
			if f.decide != nil && frame.Cmd != "aibot_subscribe" && frame.Cmd != "ping" {
				code = f.decide(n, frame)
			}
			if code < 0 {
				return
			}
			f.writeMu.Lock()
			ws.WriteJSON(map[string]any{"headers": raw["headers"], "errcode": code})
			f.writeMu.Unlock()
			if frame.Cmd == "aibot_subscribe" {
				f.ready <- n
			}
		}
	}))
	return f
}

func (f *fakeWeCom) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeWeCom) waitConn(n int) {
	f.t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case got := <-f.ready:
			if got >= n {
				return
			}
		case <-deadline:
			f.t.Fatalf("connection %d not authenticated", n)
		}
	}
}

func (f *fakeWeCom) deliver(msgID, chatType, chat, sender, text string) {
	f.t.Helper()
	body := map[string]any{"msgid": msgID, "aibotid": "ext-bot", "chatid": chat, "chattype": chatType, "from": map[string]string{"userid": sender}, "msgtype": "text", "text": map[string]string{"content": text}}
	f.mu.Lock()
	ws := f.current
	f.mu.Unlock()
	f.writeMu.Lock()
	err := ws.WriteJSON(map[string]any{"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": "req-" + msgID}, "body": body})
	f.writeMu.Unlock()
	if err != nil {
		f.t.Fatal(err)
	}
}

// replyFrames returns frames that carry the final reply text: finished stream
// frames and markdown sends.
func (f *fakeWeCom) replyFrames(text string) []wecomFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []wecomFrame
	for _, frame := range f.frames {
		switch frame.Cmd {
		case "aibot_respond_msg":
			stream, _ := frame.Body["stream"].(map[string]any)
			if finish, _ := stream["finish"].(bool); finish && stream["content"] == text {
				out = append(out, frame)
			}
		case "aibot_send_msg":
			markdown, _ := frame.Body["markdown"].(map[string]any)
			if markdown["content"] == text {
				out = append(out, frame)
			}
		}
	}
	return out
}

func isFinalStream(frame wecomFrame) bool {
	if frame.Cmd != "aibot_respond_msg" {
		return false
	}
	stream, _ := frame.Body["stream"].(map[string]any)
	finish, _ := stream["finish"].(bool)
	return finish
}

func startBaselineService(t *testing.T, fake *fakeWeCom, generate Generate) (*repo.Store, domain.NotificationBot, func()) {
	t.Helper()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	bot := domain.NotificationBot{ID: "cfg-baseline", BotID: "ext-bot", ProjectID: "p", OwnerID: 7, Enabled: true, InteractionEnabled: true}
	if err = store.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	service := New(store)
	service.URL = fake.url()
	service.Generate = generate
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	return store, bot, func() { cancel(); <-service.Done; fake.srv.Close(); store.Close() }
}

func interactionStatus(store *repo.Store, id string) string {
	item, err := store.BotInteraction(id)
	if err != nil {
		return ""
	}
	return item.Status
}

// Stored WeCom bots key messages, bindings and the connection lease by the
// external Bot ID, and conversations by the internal configuration ID.
func TestWeComBaselineIdentifiers(t *testing.T) {
	fake := newFakeWeCom(t, nil)
	store, bot, stop := startBaselineService(t, fake, func(ctx context.Context, b domain.NotificationBot, c domain.BotConversation, h []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		return "ok:" + item.Content, nil
	})
	defer stop()
	fake.waitConn(1)
	fake.deliver("m-single", "single", "", "alice", "hello")
	fake.deliver("m-group", "group", "chat-1", "bob", "hi")

	singleID := baselineSHA("ext-bot", "m-single")
	groupID := baselineSHA("ext-bot", "m-group")
	if repo.BotMessageID("ext-bot", "m-single") != singleID {
		t.Fatal("message id derivation changed")
	}
	eventually(t, func() bool {
		return interactionStatus(store, singleID) == "replied" && interactionStatus(store, groupID) == "replied"
	})
	single, _ := store.BotInteraction(singleID)
	group, _ := store.BotInteraction(groupID)
	if single.BotID != bot.ID || group.BotID != bot.ID {
		t.Fatalf("interaction bot scope: %q %q", single.BotID, group.BotID)
	}
	if single.ConversationID != baselineSHA(bot.ID, "single", "alice") || group.ConversationID != baselineSHA(bot.ID, "group", "chat-1") {
		t.Fatal("conversation id derivation changed")
	}
	conn, err := store.BotConnectionState("ext-bot")
	if err != nil || conn.State != "connected" {
		t.Fatalf("lease row keyed by external bot id: %+v %v", conn, err)
	}
}

// When WeCom rejects the final stream frame, the reply is sent once more as a
// markdown message and the interaction is marked replied.
func TestWeComBaselineRejectedStreamFallsBackToSend(t *testing.T) {
	fake := newFakeWeCom(t, func(conn int, f wecomFrame) int {
		if isFinalStream(f) {
			return 40008
		}
		return 0
	})
	store, _, stop := startBaselineService(t, fake, func(ctx context.Context, b domain.NotificationBot, c domain.BotConversation, h []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		return "答复", nil
	})
	defer stop()
	fake.waitConn(1)
	fake.deliver("m1", "single", "", "alice", "问题")
	id := baselineSHA("ext-bot", "m1")
	eventually(t, func() bool { return interactionStatus(store, id) == "replied" })
	frames := fake.replyFrames("答复")
	if len(frames) != 2 || frames[0].Cmd != "aibot_respond_msg" || frames[1].Cmd != "aibot_send_msg" {
		t.Fatalf("expected rejected stream then markdown send, got %+v", frames)
	}
}

// Known duplicate risk kept unchanged in R1 (fixed in R3): if the connection
// drops while a send is awaiting its ack, the interaction stays ready and the
// reply is sent again after reconnecting, even though the platform may already
// have received the first request. The model is not called again.
func TestWeComBaselineResendsAfterConnectionLossDuringSend(t *testing.T) {
	var generated int32
	fake := newFakeWeCom(t, func(conn int, f wecomFrame) int {
		if conn == 1 && isFinalStream(f) {
			return 40008
		}
		if conn == 1 && f.Cmd == "aibot_send_msg" {
			return -1 // request received, connection lost before the ack
		}
		return 0
	})
	store, _, stop := startBaselineService(t, fake, func(ctx context.Context, b domain.NotificationBot, c domain.BotConversation, h []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		atomic.AddInt32(&generated, 1)
		return "只生成一次", nil
	})
	defer stop()
	fake.waitConn(1)
	fake.deliver("m1", "single", "", "alice", "问题")
	id := baselineSHA("ext-bot", "m1")
	fake.waitConn(2)
	eventually(t, func() bool { return interactionStatus(store, id) == "replied" })
	frames := fake.replyFrames("只生成一次")
	var onFirst, onSecond int
	for _, frame := range frames {
		if frame.Conn == 1 && frame.Cmd == "aibot_send_msg" {
			onFirst++
		}
		if frame.Conn == 2 {
			onSecond++
		}
	}
	if onFirst != 1 || onSecond != 1 {
		t.Fatalf("expected one unacknowledged send and one resend after reconnect, got %+v", frames)
	}
	if atomic.LoadInt32(&generated) != 1 {
		t.Fatalf("model called %d times", generated)
	}
}

// A duplicate platform delivery of the same msgid produces one interaction and
// one model call, even when it arrives after the reply was sent.
func TestWeComBaselineDuplicateDeliveryAfterReply(t *testing.T) {
	var generated int32
	fake := newFakeWeCom(t, nil)
	store, bot, stop := startBaselineService(t, fake, func(ctx context.Context, b domain.NotificationBot, c domain.BotConversation, h []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		atomic.AddInt32(&generated, 1)
		return "一次", nil
	})
	defer stop()
	fake.waitConn(1)
	fake.deliver("dup", "single", "", "alice", "问题")
	id := baselineSHA("ext-bot", "dup")
	eventually(t, func() bool { return interactionStatus(store, id) == "replied" })
	fake.deliver("dup", "single", "", "alice", "问题")
	time.Sleep(700 * time.Millisecond)
	records, _ := store.BotInteractionRecords(bot.ID)
	if len(records) != 1 || atomic.LoadInt32(&generated) != 1 || len(fake.replyFrames("一次")) != 1 {
		t.Fatalf("duplicate delivery: records=%d generated=%d frames=%d", len(records), generated, len(fake.replyFrames("一次")))
	}
}

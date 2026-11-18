package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeChannel is a controllable in-memory channel: tests push inbound events
// through the sink captured at Connect, decide each reply outcome, and drop
// connections to exercise reconnects.
type fakeChannel struct {
	mu      sync.Mutex
	conns   chan *fakeConn
	outcome func(OutboundMessage) SendResult
}

func newFakeChannel() *fakeChannel {
	return &fakeChannel{conns: make(chan *fakeConn, 8), outcome: func(OutboundMessage) SendResult { return SendResult{Outcome: Delivered} }}
}
func (f *fakeChannel) Kind() string { return "fake" }
func (f *fakeChannel) Verify(context.Context, domain.NotificationBot, Credential) (Verification, error) {
	return Verification{}, nil
}
func (f *fakeChannel) Connect(ctx context.Context, bot domain.NotificationBot, cred Credential, sink InboundSink) (Conn, error) {
	c := &fakeConn{channel: f, sink: sink, ctx: ctx, secret: cred.Secret, done: make(chan struct{}), replies: make(chan OutboundMessage, 16)}
	f.conns <- c
	return c, nil
}
func (f *fakeChannel) setOutcome(fn func(OutboundMessage) SendResult) {
	f.mu.Lock()
	f.outcome = fn
	f.mu.Unlock()
}

type fakeConn struct {
	channel *fakeChannel
	sink    InboundSink
	ctx     context.Context
	secret  string
	done    chan struct{}
	once    sync.Once
	replies chan OutboundMessage
}

func (c *fakeConn) Done() <-chan struct{} { return c.done }
func (c *fakeConn) Close() error          { c.once.Do(func() { close(c.done) }); return nil }
func (c *fakeConn) Reply(ctx context.Context, msg OutboundMessage) SendResult {
	c.replies <- msg
	c.channel.mu.Lock()
	fn := c.channel.outcome
	c.channel.mu.Unlock()
	return fn(msg)
}
func (c *fakeConn) push(t *testing.T, ev InboundEvent) InboundAck {
	t.Helper()
	ack, err := c.sink(c.ctx, ev)
	if err != nil {
		t.Fatalf("sink: %v", err)
	}
	return ack
}

func waitConn(t *testing.T, ch *fakeChannel) *fakeConn {
	t.Helper()
	select {
	case c := <-ch.conns:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no connection")
		return nil
	}
}
func waitReplyMsg(t *testing.T, c *fakeConn) OutboundMessage {
	t.Helper()
	select {
	case m := <-c.replies:
		return m
	case <-time.After(6 * time.Second):
		t.Fatal("no reply")
		return OutboundMessage{}
	}
}

func TestFakeChannelDrivesServiceLifecycle(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "bot-fake", BotID: "app-fake", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	if err = store.SaveNotificationBot(bot, "fake-secret"); err != nil {
		t.Fatal(err)
	}
	service := New(store)
	fake := newFakeChannel()
	service.RegisterChannel(fake)
	service.ChannelKind = func(domain.NotificationBot) string { return "fake" }
	var mu sync.Mutex
	generated := map[string]int{}
	service.Generate = func(ctx context.Context, b domain.NotificationBot, c domain.BotConversation, h []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		mu.Lock()
		generated[item.Content]++
		mu.Unlock()
		return "答复：" + item.Content, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	defer func() { cancel(); <-service.Done }()

	event := func(id, text string) InboundEvent {
		return InboundEvent{Channel: "fake", AppID: "app-fake", EventID: "evt-" + id, MessageID: id, SenderID: "u1", ChatID: "u1", ChatType: "single", MsgType: "text", Text: text}
	}
	status := func(msgID string) string {
		item, err := store.BotInteraction(repo.BotMessageID(bot.BotID, msgID))
		if err != nil {
			return ""
		}
		return item.Status
	}

	conn := waitConn(t, fake)
	if conn.secret != "fake-secret" {
		t.Fatalf("credential not passed to Connect: %q", conn.secret)
	}
	// Delivered → replied; the duplicate business message is acknowledged
	// without a second interaction or model call.
	if ack := conn.push(t, event("m1", "第一句")); !ack.Accepted || ack.InteractionID != repo.BotMessageID(bot.BotID, "m1") {
		t.Fatalf("first ack = %+v", ack)
	}
	if msg := waitReplyMsg(t, conn); msg.Text != "答复：第一句" || msg.ChatID != "u1" || msg.ChatType != "single" {
		t.Fatalf("reply = %+v", msg)
	}
	eventually(t, func() bool { return status("m1") == "replied" })
	dup := event("m1", "第一句")
	dup.EventID = "evt-redelivered"
	if ack := conn.push(t, dup); ack.Accepted {
		t.Fatal("redelivered business message accepted twice")
	}
	// An event for another application never reaches the queue.
	foreign := event("m-foreign", "越权")
	foreign.AppID = "other-app"
	if ack := conn.push(t, foreign); ack.Accepted || ack.Ignored != "app_mismatch" {
		t.Fatalf("foreign ack = %+v", ack)
	}

	// Rejected → reply_failed with the safe detail.
	fake.setOutcome(func(OutboundMessage) SendResult {
		return SendResult{Outcome: Rejected, Code: "403", SafeDetail: "平台拒绝"}
	})
	conn.push(t, event("m2", "被拒"))
	waitReplyMsg(t, conn)
	eventually(t, func() bool { return status("m2") == "reply_failed" })
	if item, _ := store.BotInteraction(repo.BotMessageID(bot.BotID, "m2")); item.LastError != "平台拒绝" {
		t.Fatalf("reject detail = %q", item.LastError)
	}

	// Unknown → the connection is dropped and the saved reply is sent again on
	// the next connection without re-running the model (stage A behavior).
	var unknownOnce sync.Once
	fake.setOutcome(func(msg OutboundMessage) SendResult {
		result := SendResult{Outcome: Delivered}
		unknownOnce.Do(func() { result = SendResult{Outcome: Unknown, Code: "receipt_missing", SafeDetail: "回执超时"} })
		return result
	})
	conn.push(t, event("m3", "未知"))
	waitReplyMsg(t, conn)
	select {
	case <-conn.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("unknown outcome did not close the connection")
	}
	second := waitConn(t, fake)
	if msg := waitReplyMsg(t, second); msg.Text != "答复：未知" {
		t.Fatalf("resent reply = %+v", msg)
	}
	eventually(t, func() bool { return status("m3") == "replied" })

	// A dropped connection reconnects and keeps processing.
	second.Close()
	third := waitConn(t, fake)
	third.push(t, event("m4", "重连后"))
	waitReplyMsg(t, third)
	eventually(t, func() bool { return status("m4") == "replied" })

	mu.Lock()
	defer mu.Unlock()
	for text, n := range generated {
		if n != 1 {
			t.Fatalf("model ran %d times for %q", n, text)
		}
	}
	if len(generated) != 4 {
		t.Fatalf("generated = %v", generated)
	}
}

func TestUnregisteredChannelReportsNotReady(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "bot-feishu", BotID: "cli_app", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	if err = store.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	service := New(store)
	service.ChannelKind = func(domain.NotificationBot) string { return ChannelFeishu }
	if _, err := service.Channel(bot); !errors.Is(err, ErrChannelNotReady) {
		t.Fatalf("channel = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	defer func() { cancel(); <-service.Done }()
	eventually(t, func() bool {
		state, err := store.BotConnectionState(bot.BotID)
		return err == nil && state.State == "error" && state.LastError == "该渠道尚未接入"
	})
}

func TestInteractionFromEventValidation(t *testing.T) {
	bot := domain.NotificationBot{ID: "cfg", BotID: "app"}
	valid := InboundEvent{AppID: "app", MessageID: "m", SenderID: "u", ChatID: "u", ChatType: "single", Text: "hi", Route: ReplyRoute{RequestID: "req"}}
	item, reason := interactionFromEvent(bot, valid)
	if reason != "" || item.ID != repo.BotMessageID("app", "m") || item.BotID != "cfg" || item.RequestID != "req" || item.Content != "hi" {
		t.Fatalf("valid = %+v %q", item, reason)
	}
	long := strings.Repeat("x", 192)
	cases := map[string]func(*InboundEvent){
		"app_mismatch":      func(e *InboundEvent) { e.AppID = "other" },
		"missing_identity":  func(e *InboundEvent) { e.SenderID = "" },
		"invalid_chat":      func(e *InboundEvent) { e.ChatType = "channel" },
		"identity_too_long": func(e *InboundEvent) { e.ChatID = long },
		"empty":             func(e *InboundEvent) { e.Text = "  " },
	}
	for want, mutate := range cases {
		ev := valid
		mutate(&ev)
		if _, reason := interactionFromEvent(bot, ev); reason != want {
			t.Errorf("%s: reason = %q", want, reason)
		}
	}
	action := valid
	action.Text, action.Action, action.DeliveryID = "", "approve", "d"
	if _, reason := interactionFromEvent(bot, action); reason != "" {
		t.Fatalf("card action without text rejected: %q", reason)
	}
}

func TestWeComChannelInjectedVerifyAndPush(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)
	bot := domain.NotificationBot{ID: "cfg", BotID: "wecom-bot"}
	ch, err := service.Channel(bot)
	if err != nil || ch.Kind() != ChannelWeCom {
		t.Fatalf("default channel = %v %v", ch, err)
	}
	if _, err := ch.Verify(context.Background(), bot, Credential{Secret: "s"}); !errors.Is(err, ErrChannelNotReady) {
		t.Fatalf("unconfigured verify = %v", err)
	}
	if got := ch.(PushSender).Push(context.Background(), bot, Credential{Secret: "s"}, OutboundMessage{ChatID: "c", Text: "t"}); got.Outcome != NotDelivered {
		t.Fatalf("unconfigured push = %+v", got)
	}
	var verified, pushed []string
	service.ConfigureWeCom(func(ctx context.Context, botID, secret string) error {
		verified = append(verified, botID, secret)
		return nil
	}, func(ctx context.Context, botID, secret, target, content string) SendResult {
		pushed = append(pushed, botID, secret, target, content)
		return SendResult{Outcome: Delivered}
	})
	ch, _ = service.Channel(bot)
	if _, err := ch.Verify(context.Background(), bot, Credential{Secret: "s"}); err != nil || strings.Join(verified, ",") != "wecom-bot,s" {
		t.Fatalf("verify = %v %v", err, verified)
	}
	if got := ch.(PushSender).Push(context.Background(), bot, Credential{Secret: "s"}, OutboundMessage{ChatID: "chat", Text: "hello"}); got.Outcome != Delivered || strings.Join(pushed, ",") != "wecom-bot,s,chat,hello" {
		t.Fatalf("push = %+v %v", got, pushed)
	}
}

func TestClassifyWeComSend(t *testing.T) {
	cases := []struct {
		err  error
		want SendOutcome
		code string
	}{
		{nil, Delivered, ""},
		{&ReplyError{Code: 40008}, Rejected, "40008"},
		{errorsStreamUnavailable, NotDelivered, "stream_unavailable"},
		{&sendError{phase: phaseBeforeWrite, err: context.Canceled}, NotDelivered, "not_written"},
		{&sendError{phase: phaseWrite, err: errors.New("企业微信回复连接中断")}, Unknown, "write_interrupted"},
		{&sendError{phase: phaseAwaitAck, err: errors.New("企业微信回复回执超时")}, Unknown, "receipt_missing"},
		{errors.New("other"), Unknown, "unclassified"},
	}
	for _, tc := range cases {
		got := classifyWeComSend(tc.err)
		if got.Outcome != tc.want || got.Code != tc.code {
			t.Errorf("%v: got %+v", tc.err, got)
		}
		if tc.err != nil && got.SafeDetail != tc.err.Error() {
			t.Errorf("%v: detail %q", tc.err, got.SafeDetail)
		}
	}
}

// TestSocketRequestPhases checks the evidence each request failure carries:
// nothing written, written without receipt, or an explicit platform code.
func TestSocketRequestPhases(t *testing.T) {
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			switch frame.Cmd {
			case "silent": // written but never acknowledged
				continue
			case "drop":
				return
			case "reject":
				_ = ws.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 93000})
			default:
				_ = ws.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 0})
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock, err := dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "bot", "secret", func(*socket, Frame) {})
	if err != nil {
		t.Fatal(err)
	}
	defer sock.close()

	if got := classifyWeComSend(sock.request(ctx, "ok", "", nil)); got.Outcome != Delivered {
		t.Fatalf("ok = %+v", got)
	}
	if got := classifyWeComSend(sock.request(ctx, "reject", "", nil)); got.Outcome != Rejected || got.Code != "93000" {
		t.Fatalf("reject = %+v", got)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	err = sock.request(canceled, "ok", "", nil)
	if got := classifyWeComSend(err); got.Outcome != NotDelivered || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled before write = %+v %v", got, err)
	}
	waiting, stopWaiting := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stopWaiting()
	err = sock.request(waiting, "silent", "", nil)
	if got := classifyWeComSend(err); got.Outcome != Unknown || got.Code != "receipt_missing" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("written without receipt = %+v %v", got, err)
	}
	if got := classifyWeComSend(sock.request(ctx, "drop", "", nil)); got.Outcome != Unknown {
		t.Fatalf("connection closed after write = %+v", got)
	}
}

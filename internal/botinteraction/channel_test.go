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

	// Unknown → reply_unknown on a non-idempotent channel: no automatic
	// resend and the connection stays up. A manual resend starts a new
	// generation with a new key and sends only the saved reply.
	sends := 0
	var keys []string
	fake.setOutcome(func(msg OutboundMessage) SendResult {
		sends++
		keys = append(keys, msg.SendKey)
		if sends == 1 {
			return SendResult{Outcome: Unknown, Code: "receipt_missing", SafeDetail: "回执超时"}
		}
		return SendResult{Outcome: Delivered, PlatformMessageID: "pm-1"}
	})
	conn.push(t, event("m3", "未知"))
	waitReplyMsg(t, conn)
	eventually(t, func() bool { return status("m3") == "reply_unknown" })
	select {
	case <-conn.Done():
		t.Fatal("unknown outcome dropped the connection")
	case msg := <-conn.replies:
		t.Fatalf("unknown outcome was resent automatically: %+v", msg)
	case <-time.After(time.Second):
	}
	resent, err := store.ResendBotInteraction(repo.BotMessageID(bot.BotID, "m3"), 7, "action-1", "用户未收到")
	if err != nil || resent.DeliveryGeneration != 2 {
		t.Fatalf("resend = %+v %v", resent, err)
	}
	if again, err := store.ResendBotInteraction(resent.ID, 7, "action-1", "重复点击"); err != nil || again.DeliveryGeneration != 2 {
		t.Fatalf("duplicate action = %+v %v", again, err)
	}
	if msg := waitReplyMsg(t, conn); msg.Text != "答复：未知" {
		t.Fatalf("resent reply = %+v", msg)
	}
	eventually(t, func() bool { return status("m3") == "replied" })
	if len(keys) != 2 || keys[0] == "" || keys[0] == keys[1] {
		t.Fatalf("send keys = %v", keys)
	}
	if item, _ := store.BotInteraction(resent.ID); item.PlatformMessageID != "pm-1" || item.ResentBy != 7 {
		t.Fatalf("receipt = %+v", item)
	}
	fake.setOutcome(func(OutboundMessage) SendResult { return SendResult{Outcome: Delivered} })

	// A dropped connection reconnects and keeps processing.
	conn.Close()
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
	service.ChannelKind = func(domain.NotificationBot) string { return "unregistered-kind" }
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

// kindChannel registers a fake adapter under a real channel kind.
type kindChannel struct {
	*fakeChannel
	kind string
}

func (k kindChannel) Kind() string { return k.kind }

// The Feishu private-chat loop through the common service: tenant discovery
// answers with a fixed notice until an administrator locks the scope; then
// text is processed once per business message, other tenants are dropped,
// group chats are ignored and non-text gets a notice without the model.
func TestFeishuPrivateChatLoopWithTenantConfirmation(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "cfg-fs", Channel: domain.BotChannelFeishu, BotID: "cli_app", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	saved, err := store.SaveNotificationBotChecked(bot, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	service := New(store)
	fake := newFakeChannel()
	service.RegisterChannel(kindChannel{fake, ChannelFeishu})
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
	conn := waitConn(t, fake)
	event := func(eventID, msgID, tenant, chatType, msgType, text string) InboundEvent {
		return InboundEvent{Channel: ChannelFeishu, AppID: "cli_app", TenantKey: tenant, EventID: eventID, MessageID: msgID, SenderID: "ou_1", ChatID: "oc_1", ChatType: chatType, MsgType: msgType, Text: text, Route: ReplyRoute{MessageID: msgID}}
	}

	// Before confirmation: candidate recorded, fixed notice, no model call.
	conn.push(t, event("e1", "om_1", "tenant-1", "single", "text", "项目状态"))
	if msg := waitReplyMsg(t, conn); msg.Text != noticeTenantPending || msg.Route.MessageID != "om_1" {
		t.Fatalf("pending notice = %+v", msg)
	}
	pending, _ := store.NotificationBot(bot.ID)
	if pending.TenantCandidate == nil || pending.TenantCandidate.TenantKey != "tenant-1" || pending.ConfigVersion != saved.ConfigVersion {
		t.Fatalf("candidate = %+v", pending)
	}
	if _, err := store.ConfirmBotTenant(bot.ID, "tenant-1", pending.ConfigVersion, 1); err != nil {
		t.Fatal(err)
	}
	// Locking does not reconnect.
	select {
	case <-conn.Done():
		t.Fatal("tenant lock reconnected")
	case <-time.After(500 * time.Millisecond):
	}

	conn.push(t, event("e2", "om_2", "tenant-1", "single", "text", "项目状态"))
	if msg := waitReplyMsg(t, conn); msg.Text != "答复：项目状态" || msg.SendKey == "" || msg.Route.MessageID != "om_2" {
		t.Fatalf("reply = %+v", msg)
	}
	// Redelivery with a new event ID is the same business message.
	if ack := conn.push(t, event("e2-retry", "om_2", "tenant-1", "single", "text", "项目状态")); ack.Accepted {
		t.Fatal("redelivered message accepted twice")
	}
	// Another enterprise, a group message without @: dropped without a reply.
	if ack := conn.push(t, event("e3", "om_3", "tenant-x", "single", "text", "越权")); ack.Accepted || ack.Ignored != "tenant_mismatch" {
		t.Fatalf("foreign tenant = %+v", ack)
	}
	if ack := conn.push(t, event("e4", "om_4", "tenant-1", "group", "text", "群聊")); ack.Accepted || ack.Ignored != "group_not_mentioned" {
		t.Fatalf("group = %+v", ack)
	}
	// A group message that @ the Bot is answered in the group, in a
	// conversation of its own.
	mentioned := event("e4b", "om_4b", "tenant-1", "group", "text", "群里问进度")
	mentioned.ChatID, mentioned.Mentioned = "oc_group", true
	if ack := conn.push(t, mentioned); !ack.Accepted {
		t.Fatalf("mentioned group = %+v", ack)
	}
	if msg := waitReplyMsg(t, conn); msg.Text != "答复：群里问进度" || msg.ChatID != "oc_group" || msg.ChatType != "group" || msg.Route.MessageID != "om_4b" {
		t.Fatalf("group reply = %+v", msg)
	}
	// Non-text: a notice, the content never reaches the model.
	conn.push(t, event("e5", "om_5", "tenant-1", "single", "image", ""))
	if msg := waitReplyMsg(t, conn); msg.Text != noticeUnsupported {
		t.Fatalf("image notice = %+v", msg)
	}
	select {
	case msg := <-conn.replies:
		t.Fatalf("unexpected reply %+v", msg)
	case <-time.After(500 * time.Millisecond):
	}
	mu.Lock()
	defer mu.Unlock()
	if len(generated) != 2 || generated["项目状态"] != 1 || generated["群里问进度"] != 1 {
		t.Fatalf("model calls = %v", generated)
	}
	// Records use the configuration as identity scope.
	if item, err := store.BotInteraction(repo.BotMessageID(bot.ID, "om_2")); err != nil || item.Status != domain.BotReplied || item.ProjectID != "p" || item.PlatformEventID != "e2" {
		t.Fatalf("record = %+v %v", item, err)
	}
	state, _ := store.BotConnectionState(saved.LeaseKey())
	if state.IgnoredGroupMessages != 1 || !strings.HasPrefix(saved.LeaseKey(), "feishu:") {
		t.Fatalf("connection = %+v key=%s", state, saved.LeaseKey())
	}
}

// The DingTalk private-chat loop through the common service: the CorpId the
// administrator entered locks on the first matching event (and that message
// is processed), the session webhook is stored sealed and handed back to the
// adapter, an expired route ends as route_expired with the reply kept, and
// an unknown outcome is not resent automatically.
func TestDingTalkPrivateChatLoop(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bot := domain.NotificationBot{ID: "cfg-dt", Channel: domain.BotChannelDingTalk, BotID: "ding-app", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true, TenantExpected: "ding-corp", TenantExpectedBy: 7}
	saved, err := store.SaveNotificationBotChecked(bot, "secret", nil)
	if err != nil || saved.TenantState != domain.BotTenantAwaiting {
		t.Fatal(saved, err)
	}
	service := New(store)
	fake := newFakeChannel()
	service.RegisterChannel(kindChannel{fake, ChannelDingTalk})
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
	conn := waitConn(t, fake)
	webhook := "https://oapi.dingtalk.com/robot/sendBySession?session=s-"
	event := func(msgID, corp, text string, expires time.Time) InboundEvent {
		return InboundEvent{Channel: ChannelDingTalk, AppID: "ding-app", TenantKey: corp, EventID: "f-" + msgID, MessageID: msgID, SenderID: "staff-1", ChatID: "cid-1", ChatType: "single", MsgType: "text", Text: text, Route: ReplyRoute{Webhook: webhook + msgID, ExpiresAt: expires}}
	}
	later := time.Now().Add(time.Hour)

	conn.push(t, event("m1", "ding-corp", "项目状态", later))
	msg := waitReplyMsg(t, conn)
	if msg.Text != "答复：项目状态" || msg.Route.Webhook != webhook+"m1" {
		t.Fatalf("first message = %+v", msg)
	}
	locked, _ := store.NotificationBot(bot.ID)
	if locked.TenantState != domain.BotTenantLocked || locked.TenantKey != "ding-corp" || locked.TenantConfirmedBy != 7 || locked.ConfigVersion != saved.ConfigVersion+1 {
		t.Fatalf("auto lock = %+v", locked)
	}
	// Sealed at rest, bound to the interaction; cleared once replied.
	id1 := repo.BotMessageID(bot.ID, "m1")
	eventually(t, func() bool { it, _ := store.BotInteraction(id1); return it.Status == domain.BotReplied })
	if it, _ := store.BotInteraction(id1); it.ReplyRoute != "" || it.RouteExpiresAtUS == 0 {
		t.Fatalf("replied route = %q %d", it.ReplyRoute, it.RouteExpiresAtUS)
	}
	// Another enterprise after locking: dropped.
	if ack := conn.push(t, event("m2", "other-corp", "越权", later)); ack.Accepted || ack.Ignored != "tenant_mismatch" {
		t.Fatalf("foreign corp = %+v", ack)
	}
	// Unknown outcome: reply_unknown, no automatic resend.
	fake.setOutcome(func(OutboundMessage) SendResult {
		return SendResult{Outcome: Unknown, Code: "transport", SafeDetail: "未返回结果"}
	})
	conn.push(t, event("m3", "ding-corp", "未知", later))
	waitReplyMsg(t, conn)
	id3 := repo.BotMessageID(bot.ID, "m3")
	eventually(t, func() bool { it, _ := store.BotInteraction(id3); return it.Status == domain.BotReplyUnknown })
	select {
	case m := <-conn.replies:
		t.Fatalf("unknown outcome resent: %+v", m)
	case <-time.After(time.Second):
	}
	if it, _ := store.BotInteraction(id3); !strings.HasPrefix(it.ReplyRoute, "lf1:") || strings.Contains(it.ReplyRoute, "session") {
		t.Fatalf("pending route must stay sealed for a manual resend: %q", it.ReplyRoute)
	}
	// Route already expiring: the reply is kept, the status is route_expired.
	fake.setOutcome(func(OutboundMessage) SendResult { return SendResult{Outcome: Delivered} })
	conn.push(t, event("m4", "ding-corp", "过期", time.Now().Add(5*time.Second)))
	id4 := repo.BotMessageID(bot.ID, "m4")
	eventually(t, func() bool { it, _ := store.BotInteraction(id4); return it.Status == domain.BotReplyFailed })
	if it, _ := store.BotInteraction(id4); it.TerminalReason != "route_expired" || it.Reply != "答复：过期" || it.ReplyRoute != "" {
		t.Fatalf("expired = %+v", it)
	}
	if _, err := store.ResendBotInteraction(id4, 7, "k-1", "x"); !errors.Is(err, repo.ErrBotRouteExpired) {
		t.Fatalf("resend expired = %v", err)
	}
	select {
	case m := <-conn.replies:
		t.Fatalf("expired route was sent: %+v", m)
	default:
	}
}

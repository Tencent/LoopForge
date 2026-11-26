package botinteraction

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

const dingtalkWebhook = "https://oapi.dingtalk.com/robot/sendBySession?session=s1"

func dingtalkCallbackJSON(msgID, convType, msgType, text string, extra map[string]any) string {
	m := map[string]any{
		"conversationId": "cid-1", "chatbotCorpId": "ding-corp", "senderCorpId": "ding-corp",
		"msgId": msgID, "senderStaffId": "staff-1", "senderId": "$:LWCP_v1:$x", "conversationType": convType,
		"sessionWebhook": dingtalkWebhook, "sessionWebhookExpiredTime": int64(1_900_000_000_000), "msgtype": msgType,
	}
	if msgType == "text" {
		m["text"] = map[string]string{"content": text}
	}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func TestDingTalkEventMapping(t *testing.T) {
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelDingTalk, BotID: "ding-app"}
	ev, reason := dingtalkEvent(bot, dingtalkCallbackJSON("msg-1", "1", "text", "  项目状态 ", nil), "frame-1", officialDingTalkWebhook)
	if reason != "" || ev.AppID != "ding-app" || ev.TenantKey != "ding-corp" || ev.MessageID != "msg-1" || ev.EventID != "frame-1" ||
		ev.SenderID != "staff-1" || ev.ChatID != "cid-1" || ev.ChatType != "single" || ev.MsgType != "text" || ev.Text != "项目状态" ||
		ev.Route.Webhook != dingtalkWebhook || !ev.Route.ExpiresAt.Equal(time.UnixMilli(1_900_000_000_000).UTC()) {
		t.Fatalf("event = %+v %s", ev, reason)
	}
	if ev, _ := dingtalkEvent(bot, dingtalkCallbackJSON("m", "2", "text", "@bot hi", nil), "f", officialDingTalkWebhook); ev.ChatType != "group" || !ev.Mentioned {
		t.Fatalf("group = %+v", ev)
	}
	if ev, _ := dingtalkEvent(bot, dingtalkCallbackJSON("m", "2", "text", "hi", map[string]any{"isInAtList": false}), "f", officialDingTalkWebhook); ev.Mentioned {
		t.Fatalf("group without @ = %+v", ev)
	}
	if ev, _ := dingtalkEvent(bot, dingtalkCallbackJSON("m", "2", "text", "hi", map[string]any{"isInAtList": true}), "f", officialDingTalkWebhook); !ev.Mentioned {
		t.Fatalf("group with @ = %+v", ev)
	}
	if ev, _ := dingtalkEvent(bot, dingtalkCallbackJSON("m", "1", "picture", "", nil), "f", officialDingTalkWebhook); ev.MsgType != "picture" || ev.Text != "" {
		t.Fatalf("picture = %+v", ev)
	}
	// robotCode, when present, must name this application (checked by the
	// common inbound path as app_mismatch).
	if ev, _ := dingtalkEvent(bot, dingtalkCallbackJSON("m", "1", "text", "x", map[string]any{"robotCode": "other-app"}), "f", officialDingTalkWebhook); ev.AppID != "other-app" {
		t.Fatalf("robotCode = %+v", ev)
	}
	for name, extra := range map[string]map[string]any{
		"missing_staff_id":     {"senderStaffId": nil},
		"sender_corp_mismatch": {"senderCorpId": "other-corp"},
		"untrusted_webhook":    {"sessionWebhook": "https://evil.example/robot/sendBySession?session=s"},
	} {
		if _, reason := dingtalkEvent(bot, dingtalkCallbackJSON("m", "1", "text", "x", extra), "f", officialDingTalkWebhook); reason != name {
			t.Errorf("%s: reason = %q", name, reason)
		}
	}
	if _, reason := dingtalkEvent(bot, "{", "f", officialDingTalkWebhook); reason != "undecodable" {
		t.Fatal(reason)
	}
	for _, raw := range []string{"http://oapi.dingtalk.com/robot/sendBySession?session=s", "https://oapi.dingtalk.com.evil/robot/sendBySession", "https://oapi.dingtalk.com/robot/send?access_token=x", "https://u:p@oapi.dingtalk.com/robot/sendBySession"} {
		u, _ := url.Parse(raw)
		if officialDingTalkWebhook(u) {
			t.Errorf("accepted %s", raw)
		}
	}
}

// fakeDingTalk plays the open API and the Stream gateway.
type fakeDingTalk struct {
	t          *testing.T
	srv        *httptest.Server
	conns      chan *websocket.Conn
	acks       chan payload.DataFrameResponse
	closed     chan struct{}
	endpointOK atomic.Bool
	noPong     atomic.Bool
	mu         sync.Mutex
	writeMu    sync.Mutex
	webhook    func(w http.ResponseWriter, r *http.Request)
	requests   []map[string]any
}

func newFakeDingTalk(t *testing.T) *fakeDingTalk {
	f := &fakeDingTalk{t: t, conns: make(chan *websocket.Conn, 4), acks: make(chan payload.DataFrameResponse, 64), closed: make(chan struct{}, 4)}
	f.endpointOK.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["appKey"] != "ding-app" || in["appSecret"] != "secret" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"code": "invalidClientIdOrSecret", "message": "x"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok", "expireIn": 7200})
	})
	mux.HandleFunc("/v1.0/gateway/connections/open", func(w http.ResponseWriter, r *http.Request) {
		var in payload.ConnectionEndpointRequest
		json.NewDecoder(r.Body).Decode(&in)
		topics := []string{}
		for _, s := range in.Subscriptions {
			topics = append(topics, s.Type+":"+s.Topic)
		}
		if !f.endpointOK.Load() || in.ClientId != "ding-app" || in.ClientSecret != "secret" || !strings.Contains(strings.Join(topics, ","), "CALLBACK:"+payload.BotMessageCallbackTopic) {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(payload.ConnectionEndpointResponse{Endpoint: "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/stream", Ticket: "ticket-1"})
	})
	var upgrader websocket.Upgrader
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != "ticket-1" {
			w.WriteHeader(403)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if f.noPong.Load() {
			c.SetPingHandler(func(string) error { return nil })
		}
		f.conns <- c
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				f.closed <- struct{}{}
				return
			}
			var resp payload.DataFrameResponse
			if json.Unmarshal(raw, &resp) == nil {
				f.acks <- resp
			}
		}
	})
	mux.HandleFunc("/robot/sendBySession", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.requests = append(f.requests, body)
		h := f.webhook
		f.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"errcode": 0, "errmsg": "ok"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDingTalk) channel() *dingtalkChannel {
	ch := newDingTalkChannel()
	ch.api = f.srv.URL
	ch.allowInsecureStream = true
	ch.keepAlive = 200 * time.Millisecond
	ch.pongWait = 300 * time.Millisecond
	ch.ackTimeout = 300 * time.Millisecond
	host := strings.TrimPrefix(f.srv.URL, "http://")
	ch.webhookAllowed = func(u *url.URL) bool { return u.Host == host && u.Path == "/robot/sendBySession" }
	return ch
}

func (f *fakeDingTalk) webhookURL() string { return f.srv.URL + "/robot/sendBySession?session=s1" }

func (f *fakeDingTalk) send(c *websocket.Conn, typ, topic, id, data string) {
	f.t.Helper()
	frame := payload.DataFrame{SpecVersion: "1.0", Type: typ, Time: time.Now().UnixMilli(), Headers: payload.DataFrameHeader{"topic": topic, "messageId": id, "contentType": "application/json"}, Data: data}
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	if err := c.WriteMessage(websocket.TextMessage, frame.Encode()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeDingTalk) ack(t *testing.T) payload.DataFrameResponse {
	t.Helper()
	select {
	case a := <-f.acks:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("no ack")
		return payload.DataFrameResponse{}
	}
}

func waitClosed(t *testing.T, done <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("%s: connection not closed within %s", what, within)
	}
}

var dingtalkBot = domain.NotificationBot{ID: "cfg-dt", Channel: domain.BotChannelDingTalk, BotID: "ding-app"}

func TestDingTalkVerify(t *testing.T) {
	f := newFakeDingTalk(t)
	ch := f.channel()
	if _, err := ch.Verify(context.Background(), dingtalkBot, Credential{Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Verify(context.Background(), dingtalkBot, Credential{Secret: "bad"}); err == nil || !strings.Contains(err.Error(), "invalidClientIdOrSecret") {
		t.Fatalf("bad secret = %v", err)
	}
}

func TestDingTalkStreamPingCallbackAndAck(t *testing.T) {
	f := newFakeDingTalk(t)
	var mu sync.Mutex
	var got []InboundEvent
	mode := "ok"
	sink := func(ctx context.Context, ev InboundEvent) (InboundAck, error) {
		mu.Lock()
		m := mode
		got = append(got, ev)
		mu.Unlock()
		switch m {
		case "fail":
			return InboundAck{}, errors.New("database unavailable")
		case "block":
			time.Sleep(2 * time.Second)
		}
		return InboundAck{Accepted: true}, nil
	}
	ch := f.channel()
	ch.webhookAllowed = officialDingTalkWebhook // fixtures carry the official address
	conn, err := ch.Connect(context.Background(), dingtalkBot, Credential{Secret: "secret"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	server := <-f.conns

	f.send(server, "SYSTEM", "ping", "ping-1", `{"opaque":"42"}`)
	if a := f.ack(t); a.Code != 200 || a.Headers["messageId"] != "ping-1" || a.Data != `{"opaque":"42"}` {
		t.Fatalf("pong = %+v", a)
	}
	f.send(server, "CALLBACK", payload.BotMessageCallbackTopic, "frame-1", dingtalkCallbackJSON("msg-1", "1", "text", "你好", nil))
	if a := f.ack(t); a.Code != 200 || a.Headers["messageId"] != "frame-1" {
		t.Fatalf("stored ack = %+v", a)
	}
	mu.Lock()
	mode = "fail"
	mu.Unlock()
	f.send(server, "CALLBACK", payload.BotMessageCallbackTopic, "frame-2", dingtalkCallbackJSON("msg-2", "1", "text", "再来", nil))
	if a := f.ack(t); a.Code != 500 || a.Headers["messageId"] != "frame-2" {
		t.Fatalf("failed ack = %+v", a)
	}
	mu.Lock()
	mode = "block"
	mu.Unlock()
	start := time.Now()
	f.send(server, "CALLBACK", payload.BotMessageCallbackTopic, "frame-3", dingtalkCallbackJSON("msg-3", "1", "text", "慢", nil))
	if a := f.ack(t); a.Code != 500 || time.Since(start) > time.Second {
		t.Fatalf("blocked sink ack = %+v after %s", a, time.Since(start))
	}
	// Dropped callbacks (no staff id) and unknown topics are still answered.
	f.send(server, "CALLBACK", payload.BotMessageCallbackTopic, "frame-4", dingtalkCallbackJSON("msg-4", "1", "text", "x", map[string]any{"senderStaffId": nil}))
	if a := f.ack(t); a.Code != 200 {
		t.Fatalf("dropped ack = %+v", a)
	}
	f.send(server, "EVENT", "chat_update", "frame-5", "{}")
	if a := f.ack(t); a.Code != 404 || a.Headers["messageId"] != "frame-5" {
		t.Fatalf("unknown topic = %+v", a)
	}
	mu.Lock()
	if len(got) != 3 || got[0].MessageID != "msg-1" || got[0].Text != "你好" || got[0].Route.Webhook != dingtalkWebhook {
		t.Fatalf("events = %+v", got)
	}
	mu.Unlock()
}

func TestDingTalkStreamLifecycle(t *testing.T) {
	sink := func(ctx context.Context, ev InboundEvent) (InboundAck, error) { return InboundAck{Accepted: true}, nil }
	t.Run("server disconnect frame", func(t *testing.T) {
		f := newFakeDingTalk(t)
		conn, err := f.channel().Connect(context.Background(), dingtalkBot, Credential{Secret: "secret"}, sink)
		if err != nil {
			t.Fatal(err)
		}
		server := <-f.conns
		f.send(server, "SYSTEM", "disconnect", "d-1", "{}")
		waitClosed(t, conn.Done(), 2*time.Second, "disconnect")
	})
	t.Run("server stops answering pings", func(t *testing.T) {
		f := newFakeDingTalk(t)
		f.noPong.Store(true)
		conn, err := f.channel().Connect(context.Background(), dingtalkBot, Credential{Secret: "secret"}, sink)
		if err != nil {
			t.Fatal(err)
		}
		server := <-f.conns
		waitClosed(t, conn.Done(), 2*time.Second, "pong timeout")
		// The defect of the SDK loop: a late frame after the timeout. It must
		// be harmless here (the process would crash with the SDK).
		f.writeMu.Lock()
		_ = server.WriteMessage(websocket.TextMessage, []byte(`{"type":"SYSTEM","headers":{"topic":"ping","messageId":"late"}}`))
		f.writeMu.Unlock()
		time.Sleep(200 * time.Millisecond)
	})
	t.Run("pongs keep the connection alive", func(t *testing.T) {
		f := newFakeDingTalk(t)
		conn, err := f.channel().Connect(context.Background(), dingtalkBot, Credential{Secret: "secret"}, sink)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		select {
		case <-conn.Done():
			t.Fatal("healthy connection closed")
		case <-time.After(1500 * time.Millisecond):
		}
	})
	t.Run("context cancel", func(t *testing.T) {
		f := newFakeDingTalk(t)
		ctx, cancel := context.WithCancel(context.Background())
		conn, err := f.channel().Connect(ctx, dingtalkBot, Credential{Secret: "secret"}, sink)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		waitClosed(t, conn.Done(), 2*time.Second, "cancel")
		select {
		case <-f.closed:
		case <-time.After(2 * time.Second):
			t.Fatal("server side not closed")
		}
	})
	t.Run("close with a blocked handler and frames in flight", func(t *testing.T) {
		f := newFakeDingTalk(t)
		release := make(chan struct{})
		defer close(release)
		blocked := func(ctx context.Context, ev InboundEvent) (InboundAck, error) { <-release; return InboundAck{}, nil }
		conn, err := f.channel().Connect(context.Background(), dingtalkBot, Credential{Secret: "secret"}, blocked)
		if err != nil {
			t.Fatal(err)
		}
		server := <-f.conns
		for i := 0; i < 20; i++ {
			f.send(server, "CALLBACK", payload.BotMessageCallbackTopic, fmt.Sprintf("f-%d", i), dingtalkCallbackJSON(fmt.Sprintf("m-%d", i), "1", "text", "x", nil))
		}
		start := time.Now()
		conn.Close()
		waitClosed(t, conn.Done(), time.Second, "close")
		if time.Since(start) > 2*time.Second {
			t.Fatalf("Close took %s", time.Since(start))
		}
		// Frames after close: nothing panics.
		f.writeMu.Lock()
		_ = server.WriteMessage(websocket.TextMessage, []byte(`{}`))
		f.writeMu.Unlock()
	})
	t.Run("rejected endpoint and bad scheme", func(t *testing.T) {
		f := newFakeDingTalk(t)
		f.endpointOK.Store(false)
		if _, err := f.channel().Connect(context.Background(), dingtalkBot, Credential{Secret: "secret"}, sink); err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("rejected endpoint = %v", err)
		}
		f.endpointOK.Store(true)
		ch := f.channel()
		ch.allowInsecureStream = false
		if _, err := ch.Connect(context.Background(), dingtalkBot, Credential{Secret: "secret"}, sink); err == nil || !strings.Contains(err.Error(), "地址无效") {
			t.Fatalf("ws:// in production mode = %v", err)
		}
	})
}

func TestDingTalkReplyClassification(t *testing.T) {
	f := newFakeDingTalk(t)
	ch := f.channel()
	msg := OutboundMessage{InteractionID: "i", Text: "答复", Route: ReplyRoute{Webhook: f.webhookURL(), ExpiresAt: time.Now().Add(time.Hour)}}
	if got := ch.replyWebhook(context.Background(), msg); got.Outcome != Delivered {
		t.Fatalf("reply = %+v", got)
	}
	f.mu.Lock()
	body := f.requests[0]
	f.mu.Unlock()
	if body["msgtype"] != "text" || body["text"].(map[string]any)["content"] != "答复" {
		t.Fatalf("body = %v", body)
	}
	cases := []struct {
		name   string
		status int
		body   string
		want   SendOutcome
	}{
		{"rate limited errcode", 200, `{"errcode":130101,"errmsg":"send too fast"}`, NotDelivered},
		{"busy", 200, `{"errcode":-1,"errmsg":"busy"}`, Unknown},
		{"other errcode", 200, `{"errcode":300001,"errmsg":"session expired"}`, Rejected},
		{"unparsed 200", 200, `<html>`, Unknown},
		{"server error", 502, ``, Unknown},
		{"too many requests", 429, ``, NotDelivered},
		{"forbidden", 403, ``, Rejected},
		{"redirect is not followed", 302, ``, Rejected},
	}
	for _, tc := range cases {
		f.mu.Lock()
		f.webhook = func(w http.ResponseWriter, r *http.Request) {
			if tc.status == 302 {
				w.Header().Set("Location", "https://evil.example/")
			}
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}
		f.mu.Unlock()
		if got := ch.replyWebhook(context.Background(), msg); got.Outcome != tc.want || got.SafeDetail == "" {
			t.Errorf("%s = %+v", tc.name, got)
		}
	}
	for name, m := range map[string]OutboundMessage{
		"route_missing":   {Text: "x"},
		"route_untrusted": {Text: "x", Route: ReplyRoute{Webhook: "https://evil.example/robot/sendBySession"}},
		"route_expired":   {Text: "x", Route: ReplyRoute{Webhook: f.webhookURL(), ExpiresAt: time.Now().Add(5 * time.Second)}},
	} {
		if got := ch.replyWebhook(context.Background(), m); got.Outcome != Rejected || got.Code != name {
			t.Errorf("%s = %+v", name, got)
		}
	}
	// Transport: refused connection sent nothing; a reset after the request
	// may have been delivered.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	ch.webhookAllowed = func(*url.URL) bool { return true }
	if got := ch.replyWebhook(context.Background(), OutboundMessage{Text: "x", Route: ReplyRoute{Webhook: deadURL + "/robot/sendBySession"}}); got.Outcome != NotDelivered {
		t.Fatalf("refused = %+v", got)
	}
	if got := classifyDingTalkTransport(errors.New("read: connection reset by peer")); got.Outcome != Unknown {
		t.Fatalf("reset = %+v", got)
	}
}

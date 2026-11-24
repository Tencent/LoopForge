package botinteraction

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/gorilla/websocket"
)

// feishuEventJSON is an im.message.receive_v1 event in the official shape.
func feishuEventJSON(eventID, appID, tenant, messageID, chatType, msgType, content string) string {
	b, _ := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]any{"event_id": eventID, "event_type": "im.message.receive_v1", "app_id": appID, "tenant_key": tenant, "create_time": "1794970000000", "token": ""},
		"event": map[string]any{
			"sender":  map[string]any{"sender_id": map[string]any{"open_id": "ou_sender", "union_id": "on_x", "user_id": "u_x"}, "sender_type": "user", "tenant_key": tenant},
			"message": map[string]any{"message_id": messageID, "chat_id": "oc_chat", "chat_type": chatType, "message_type": msgType, "content": content, "create_time": "1794970000000"},
		},
	})
	return string(b)
}

func TestFeishuEventMapping(t *testing.T) {
	var ev larkim.P2MessageReceiveV1
	if err := json.Unmarshal([]byte(feishuEventJSON("evt-1", "cli_app", "tenant-1", "om_1", "p2p", "text", `{"text":"项目状态"}`)), &ev); err != nil {
		t.Fatal(err)
	}
	got, ok := feishuEvent(&ev)
	if !ok || got.Channel != ChannelFeishu || got.AppID != "cli_app" || got.TenantKey != "tenant-1" || got.EventID != "evt-1" || got.MessageID != "om_1" || got.SenderID != "ou_sender" || got.ChatType != "single" || got.MsgType != "text" || got.Text != "项目状态" || got.Route.MessageID != "om_1" {
		t.Fatalf("event = %+v", got)
	}
	json.Unmarshal([]byte(feishuEventJSON("evt-2", "cli_app", "tenant-1", "om_2", "group", "image", `{"image_key":"img"}`)), &ev)
	got, _ = feishuEvent(&ev)
	if got.ChatType != "group" || got.MsgType != "image" || got.Text != "" {
		t.Fatalf("group image = %+v", got)
	}
	if _, ok := feishuEvent(&larkim.P2MessageReceiveV1{}); ok {
		t.Fatal("empty event accepted")
	}
}

// fakeFeishuOpenAPI serves the token, tenant and reply endpoints.
type fakeFeishuOpenAPI struct {
	mu        sync.Mutex
	tokenCode int
	replyCode int
	replyHTTP int
	uuids     []string
	texts     []string
}

func (f *fakeFeishuOpenAPI) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		code := f.tokenCode
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if code != 0 {
			json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "invalid app secret"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok", "tenant_access_token": "t-token", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/tenant/v2/tenant/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"tenant": map[string]any{"name": "测试企业", "tenant_key": "tenant-1"}}})
	})
	mux.HandleFunc("/open-apis/im/v1/messages/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/reply") || r.Header.Get("Authorization") != "Bearer t-token" {
			t.Errorf("unexpected reply request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			MsgType string `json:"msg_type"`
			Content string `json:"content"`
			UUID    string `json:"uuid"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		var content struct {
			Text string `json:"text"`
		}
		json.Unmarshal([]byte(body.Content), &content)
		f.mu.Lock()
		f.uuids = append(f.uuids, body.UUID)
		f.texts = append(f.texts, content.Text)
		code, status := f.replyCode, f.replyHTTP
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
		}
		if code != 0 {
			json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "fail"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om_reply"}})
	})
	return mux
}

func TestFeishuVerifyAndReplyClassification(t *testing.T) {
	api := &fakeFeishuOpenAPI{}
	srv := httptest.NewServer(api.handler(t))
	defer srv.Close()
	ch := newFeishuChannel()
	ch.base = srv.URL
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli_app"}
	verified, err := ch.Verify(context.Background(), bot, Credential{Secret: "s"})
	if err != nil || verified.TenantKey != "tenant-1" || verified.TenantName != "测试企业" {
		t.Fatalf("verify = %+v %v", verified, err)
	}
	api.tokenCode = 10014
	if _, err := ch.Verify(context.Background(), bot, Credential{Secret: "bad"}); err == nil || !strings.Contains(err.Error(), "10014") {
		t.Fatalf("bad secret = %v", err)
	}
	api.tokenCode = 0

	conn := &feishuConn{channel: ch, bot: bot, api: ch.client(bot, Credential{Secret: "s"})}
	msg := OutboundMessage{InteractionID: "i", Text: "答复", SendKey: "send-key-for-test", Route: ReplyRoute{MessageID: "om_1"}}
	if got := conn.Reply(context.Background(), msg); got.Outcome != Delivered || got.PlatformMessageID != "om_reply" {
		t.Fatalf("reply = %+v", got)
	}
	if api.uuids[0] != msg.SendKey || api.texts[0] != "答复" {
		t.Fatalf("uuid/text = %v %v", api.uuids, api.texts)
	}
	for code, want := range map[int]SendOutcome{99991400: NotDelivered, 99991663: NotDelivered, 230002: Rejected, 230011: Rejected} {
		api.replyCode = code
		if got := conn.Reply(context.Background(), msg); got.Outcome != want || !strings.Contains(got.SafeDetail, "错误码") {
			t.Errorf("code %d = %+v", code, got)
		}
	}
	api.replyCode, api.replyHTTP = 0, 0
	if got := conn.Reply(context.Background(), OutboundMessage{Text: "x"}); got.Outcome != Rejected || got.Code != "route_missing" {
		t.Fatalf("no route = %+v", got)
	}
	// A transport failure after the request may have been written is unknown.
	srv.Close()
	if got := conn.Reply(context.Background(), msg); got.Outcome != NotDelivered && got.Outcome != Unknown {
		t.Fatalf("closed server = %+v", got)
	}
	if got := classifyFeishuTransport(errors.New("read: connection reset")); got.Outcome != Unknown {
		t.Fatalf("reset = %+v", got)
	}
}

// fakeFeishuWS implements the long-connection bootstrap and data frames.
type fakeFeishuWS struct {
	t      *testing.T
	srv    *httptest.Server
	conns  chan *websocket.Conn
	acks   chan int
	closed chan struct{}
}

func newFakeFeishuWS(t *testing.T, api http.Handler) *fakeFeishuWS {
	f := &fakeFeishuWS{t: t, conns: make(chan *websocket.Conn, 4), acks: make(chan int, 16), closed: make(chan struct{}, 4)}
	mux := http.NewServeMux()
	mux.Handle("/open-apis/", api)
	mux.HandleFunc("/callback/ws/endpoint", func(w http.ResponseWriter, r *http.Request) {
		url := "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/ws?device_id=d1&service_id=1"
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"URL": url, "ClientConfig": map[string]any{"PingInterval": 120, "ReconnectCount": -1, "ReconnectInterval": 120, "ReconnectNonce": 1}}})
	})
	var upgrader websocket.Upgrader
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.conns <- c
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				f.closed <- struct{}{}
				return
			}
			var frame larkws.Frame
			if frame.Unmarshal(raw) != nil || frame.Method != int32(larkws.FrameTypeData) {
				continue
			}
			var resp struct {
				Code int `json:"code"`
			}
			json.Unmarshal(frame.Payload, &resp)
			f.acks <- resp.Code
		}
	})
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeFeishuWS) push(c *websocket.Conn, payload string) {
	frame := larkws.Frame{SeqID: 1, LogID: 1, Service: 1, Method: int32(larkws.FrameTypeData), Headers: []larkws.Header{{Key: "type", Value: "event"}, {Key: "message_id", Value: "frame-1"}, {Key: "sum", Value: "1"}, {Key: "seq", Value: "0"}, {Key: "trace_id", Value: "trace"}}, Payload: []byte(payload)}
	raw, _ := frame.Marshal()
	if err := c.WriteMessage(websocket.BinaryMessage, raw); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeFeishuWS) ack(t *testing.T) int {
	t.Helper()
	select {
	case code := <-f.acks:
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("no ack")
		return 0
	}
}

// The SDK acknowledges a stored event with 200 and a storage failure with
// 500 (Feishu then redelivers); Close stops the client.
func TestFeishuConnectAcknowledgesByPersistenceOutcome(t *testing.T) {
	api := &fakeFeishuOpenAPI{}
	ws := newFakeFeishuWS(t, api.handler(t))
	defer ws.srv.Close()
	ch := newFeishuChannel()
	ch.base = ws.srv.URL
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli_app"}
	var mu sync.Mutex
	var got []InboundEvent
	fail := false
	sink := func(ctx context.Context, ev InboundEvent) (InboundAck, error) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := ctx.Deadline(); !ok {
			t.Error("sink without an ack deadline")
		}
		got = append(got, ev)
		if fail {
			return InboundAck{}, errors.New("database unavailable")
		}
		return InboundAck{Accepted: true}, nil
	}
	conn, err := ch.Connect(context.Background(), bot, Credential{Secret: "s"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	server := <-ws.conns
	ws.push(server, feishuEventJSON("evt-1", "cli_app", "tenant-1", "om_1", "p2p", "text", `{"text":"你好"}`))
	if code := ws.ack(t); code != http.StatusOK {
		t.Fatalf("stored event ack = %d", code)
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	ws.push(server, feishuEventJSON("evt-2", "cli_app", "tenant-1", "om_2", "p2p", "text", `{"text":"再来"}`))
	if code := ws.ack(t); code != http.StatusInternalServerError {
		t.Fatalf("failed event ack = %d", code)
	}
	mu.Lock()
	if len(got) != 2 || got[0].MessageID != "om_1" || got[0].Text != "你好" || got[0].TenantKey != "tenant-1" {
		t.Fatalf("events = %+v", got)
	}
	mu.Unlock()
	conn.Close()
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the client")
	}
	select {
	case <-ws.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("server connection not closed")
	}
}

func TestFeishuConnectFailsWhenBootstrapRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 403, "msg": "forbidden"})
	}))
	defer srv.Close()
	ch := newFeishuChannel()
	ch.base = srv.URL
	ch.readyWait = 3 * time.Second
	start := time.Now()
	if _, err := ch.Connect(context.Background(), domain.NotificationBot{BotID: "cli_app"}, Credential{Secret: "s"}, nil); err == nil {
		t.Fatal("connected with a rejected bootstrap")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("rejected bootstrap waited for the ready timeout")
	}
}

// A sink that blocks (slow database) is answered with a failure at the ack
// deadline so Feishu redelivers, instead of holding the ack past its window.
func TestFeishuAckBoundedWhenPersistenceBlocks(t *testing.T) {
	ch := newFeishuChannel()
	ch.ackTimeout = 200 * time.Millisecond
	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	_, err := ch.acknowledge(context.Background(), InboundEvent{MessageID: "om_1"}, func(context.Context, InboundEvent) (InboundAck, error) {
		<-release
		return InboundAck{Accepted: true}, nil
	})
	if !errors.Is(err, errFeishuAckTimeout) || time.Since(start) > time.Second {
		t.Fatalf("blocked sink = %v after %s", err, time.Since(start))
	}
	if _, err := ch.acknowledge(context.Background(), InboundEvent{}, func(context.Context, InboundEvent) (InboundAck, error) { return InboundAck{}, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestFeishuGroupMentionMapping(t *testing.T) {
	raw := func(mentions []map[string]any) string {
		b, _ := json.Marshal(map[string]any{
			"schema": "2.0",
			"header": map[string]any{"event_id": "evt-g", "event_type": "im.message.receive_v1", "app_id": "cli_app", "tenant_key": "tk"},
			"event": map[string]any{
				"sender":  map[string]any{"sender_id": map[string]any{"open_id": "ou_sender"}, "sender_type": "user"},
				"message": map[string]any{"message_id": "om_g", "chat_id": "oc_group", "chat_type": "group", "message_type": "text", "content": `{"text":"@_user_1 帮我建个需求 @_user_2"}`, "mentions": mentions},
			},
		})
		return string(b)
	}
	parse := func(mentions []map[string]any) InboundEvent {
		var ev larkim.P2MessageReceiveV1
		if err := json.Unmarshal([]byte(raw(mentions)), &ev); err != nil {
			t.Fatal(err)
		}
		got, ok := feishuEvent(&ev)
		if !ok {
			t.Fatal("not mapped")
		}
		return got
	}
	got := parse([]map[string]any{{"key": "@_user_1", "name": "LoopForge", "mentioned_type": "bot"}, {"key": "@_user_2", "name": "张三", "mentioned_type": "user"}})
	if !got.Mentioned || got.Text != "帮我建个需求" || got.ChatType != "group" || got.ChatID != "oc_group" {
		t.Fatalf("bot mention = %+v", got)
	}
	if got := parse([]map[string]any{{"key": "@_user_1", "name": "张三", "mentioned_type": "user"}}); got.Mentioned {
		t.Fatal("user mention counted as @ Bot")
	}
	if got := parse(nil); got.Mentioned {
		t.Fatal("no mention counted")
	}
	if got := parse([]map[string]any{{"key": "@_user_1", "name": "LoopForge"}}); !got.Mentioned {
		t.Fatal("mention without type not counted")
	}
}

package botinteraction

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// feishuChannel connects a Feishu self-built application through the
// official long-connection client (larksuite/oapi-sdk-go v3.13.0) and
// replies with Message Reply. Only private text chats are handled; the
// common inbound path drops group events and classifies other types.
//
// Delivery: Feishu de-duplicates replies that carry the same uuid for one
// hour, so the persisted send key is passed as uuid and unknown outcomes are
// retried with that key (sendPolicy marks Feishu idempotent). The SDK itself
// retries a request only after a dial failure or an invalid tenant token,
// both with the identical body and therefore the same uuid.
type feishuChannel struct {
	// base is the open platform origin; tests point it at a local server.
	base string
	http *http.Client
	// ackTimeout bounds persistence inside the event callback so the SDK
	// acknowledges within Feishu's three-second window.
	ackTimeout time.Duration
	readyWait  time.Duration
}

func newFeishuChannel() *feishuChannel {
	return &feishuChannel{
		base:       lark.FeishuBaseUrl,
		http:       &http.Client{Timeout: sendTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		ackTimeout: 2500 * time.Millisecond,
		readyWait:  15 * time.Second,
	}
}

func (f *feishuChannel) Kind() string { return ChannelFeishu }

func (f *feishuChannel) client(bot domain.NotificationBot, cred Credential) *lark.Client {
	return lark.NewClient(bot.BotID, cred.Secret,
		lark.WithOpenBaseUrl(f.base),
		lark.WithHttpClient(f.http),
		lark.WithReqTimeout(sendTimeout),
		lark.WithEnableTokenCache(true),
		lark.WithLogLevel(larkcore.LogLevelError),
	)
}

// Verify obtains a tenant access token, which proves the App ID and Secret.
// The enterprise query is informational: its failure (for example a missing
// permission) does not fail verification. The tenant key itself is locked
// only through the trusted-event confirmation flow.
func (f *feishuChannel) Verify(ctx context.Context, bot domain.NotificationBot, cred Credential) (Verification, error) {
	cli := f.client(bot, cred)
	resp, err := cli.GetTenantAccessTokenBySelfBuiltApp(ctx, &larkcore.SelfBuiltTenantAccessTokenReq{AppID: bot.BotID, AppSecret: cred.Secret})
	if err != nil {
		return Verification{}, errors.New("飞书开放平台暂不可用，请稍后重试")
	}
	if resp.Code != 0 || resp.TenantAccessToken == "" {
		return Verification{}, fmt.Errorf("飞书拒绝了 App ID 或 App Secret（错误码 %d）", resp.Code)
	}
	out := Verification{}
	if q, err := cli.Tenant.V2.Tenant.Query(ctx); err == nil && q.Success() && q.Data != nil && q.Data.Tenant != nil {
		if q.Data.Tenant.TenantKey != nil {
			out.TenantKey = *q.Data.Tenant.TenantKey
		}
		if q.Data.Tenant.Name != nil {
			out.TenantName = *q.Data.Tenant.Name
		}
	}
	return out, nil
}

// Connect starts the long connection and returns once the SDK reports it is
// ready. The SDK's own reconnect loop is disabled: the service reconnects
// with backoff and re-reads the credential each time.
func (f *feishuChannel) Connect(ctx context.Context, bot domain.NotificationBot, cred Credential, sink InboundSink) (Conn, error) {
	conn := &feishuConn{channel: f, bot: bot, api: f.client(bot, cred), done: make(chan struct{})}
	handler := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(func(evCtx context.Context, ev *larkim.P2MessageReceiveV1) error {
		inbound, ok := feishuEvent(ev)
		if !ok {
			return nil
		}
		_, err := f.acknowledge(ctx, inbound, sink)
		return err
	}).OnP2CardActionTrigger(func(evCtx context.Context, ev *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
		inbound, ok := feishuCardEvent(ev)
		if !ok {
			return feishuToast("error", "这张卡片已失效，请到平台查看运行状态"), nil
		}
		ack, err := f.acknowledge(ctx, inbound, sink)
		if err != nil {
			return feishuToast("error", "暂时无法处理，请稍后再点一次"), nil
		}
		return feishuCardAnswer(ack), nil
	})
	ready := make(chan struct{})
	var readyOnce sync.Once
	conn.ws = larkws.NewClient(bot.BotID, cred.Secret,
		larkws.WithEventHandler(handler),
		larkws.WithAutoReconnect(false),
		larkws.WithDomain(f.base),
		larkws.WithHttpClient(f.http),
		larkws.WithLogLevel(larkcore.LogLevelError),
		larkws.WithOnReady(func() { readyOnce.Do(func() { close(ready) }) }),
	)
	runCtx, cancel := context.WithCancel(ctx)
	conn.cancel = cancel
	go func() {
		defer close(conn.done)
		conn.err = conn.ws.Start(runCtx)
	}()
	timer := time.NewTimer(f.readyWait)
	defer timer.Stop()
	select {
	case <-ready:
		return conn, nil
	case <-conn.done:
		cancel()
		return nil, errors.New("飞书长连接建立失败")
	case <-timer.C:
		conn.Close()
		return nil, errors.New("飞书长连接建立超时")
	case <-ctx.Done():
		conn.Close()
		return nil, ctx.Err()
	}
}

// errFeishuAckTimeout: persistence did not finish inside the ack budget.
var errFeishuAckTimeout = errors.New("飞书事件入库超时，等待平台重投")

// acknowledge persists an event within Feishu's three-second ack window. A
// storage error is returned so the SDK answers 500 and Feishu redelivers;
// duplicates and ignored events are acknowledged. The repository calls do not
// take a context, so the sink runs in its own goroutine and a slow database
// answers with a failure at the deadline instead of blocking past the window.
// The write may still complete afterwards; the redelivered event is then a
// duplicate of the same business message and is acknowledged without a second
// interaction.
func (f *feishuChannel) acknowledge(ctx context.Context, inbound InboundEvent, sink InboundSink) (InboundAck, error) {
	ackCtx, cancel := context.WithTimeout(ctx, f.ackTimeout)
	defer cancel()
	type result struct {
		ack InboundAck
		err error
	}
	done := make(chan result, 1)
	go func() {
		ack, err := sink(ackCtx, inbound)
		done <- result{ack, err}
	}()
	select {
	case r := <-done:
		return r.ack, r.err
	case <-ackCtx.Done():
		return InboundAck{}, errFeishuAckTimeout
	}
}

// feishuCardAnswer answers a card callback: a toast, and the updated card
// when there is one (so handled buttons disappear at once).
func feishuCardAnswer(ack InboundAck) *callback.CardActionTriggerResponse {
	kind, text := "info", ack.Toast
	if text == "" {
		text = "已收到"
	}
	if ack.Ignored != "" && ack.Card == nil {
		kind = "warning"
	}
	resp := feishuToast(kind, text)
	if ack.Card != nil {
		resp.Card = &callback.Card{Type: "raw", Data: feishuCardObject(ack.Card)}
	}
	return resp
}

// feishuEvent maps a receive event to the channel-neutral event. Text
// content is JSON ({"text": ...}); other message types keep their type and
// an empty text for classification.
func feishuEvent(ev *larkim.P2MessageReceiveV1) (InboundEvent, bool) {
	if ev == nil || ev.EventV2Base == nil || ev.EventV2Base.Header == nil || ev.Event == nil || ev.Event.Message == nil || ev.Event.Sender == nil || ev.Event.Sender.SenderId == nil {
		return InboundEvent{}, false
	}
	msg := ev.Event.Message
	out := InboundEvent{
		Channel:   ChannelFeishu,
		AppID:     ev.EventV2Base.Header.AppID,
		TenantKey: ev.EventV2Base.Header.TenantKey,
		EventID:   ev.EventV2Base.Header.EventID,
		MessageID: deref(msg.MessageId),
		SenderID:  deref(ev.Event.Sender.SenderId.OpenId),
		ChatID:    deref(msg.ChatId),
		MsgType:   deref(msg.MessageType),
	}
	switch deref(msg.ChatType) {
	case "p2p":
		out.ChatType = "single"
	case "group":
		out.ChatType = "group"
	default:
		out.ChatType = deref(msg.ChatType)
	}
	if out.MsgType == "text" {
		var content struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(deref(msg.Content)), &content) == nil {
			out.Text = content.Text
		}
	}
	// Group messages count only when they @ a bot (without the "all group
	// messages" permission Feishu delivers nothing else). The @ placeholders
	// (@_user_1 …) are removed from the text.
	mentionedBot := false
	for _, m := range msg.Mentions {
		if m == nil {
			continue
		}
		if deref(m.MentionedType) == "bot" {
			mentionedBot = true
		}
		if key := deref(m.Key); key != "" {
			out.Text = strings.ReplaceAll(out.Text, key, "")
		}
	}
	out.Text = strings.TrimSpace(out.Text)
	if out.ChatType == "group" {
		out.Mentioned = mentionedBot || (len(msg.Mentions) > 0 && !feishuMentionTypesKnown(msg.Mentions))
	}
	out.Route.MessageID = out.MessageID
	// A reply (quote) of an earlier message, e.g. a clarification card.
	out.ParentID = deref(msg.ParentId)
	return out, true
}

// feishuMentionTypesKnown reports whether the event carries mentioned_type
// (older events do not); without it any mention counts as an @ of the Bot.
func feishuMentionTypesKnown(mentions []*larkim.MentionEvent) bool {
	for _, m := range mentions {
		if m != nil && m.MentionedType != nil {
			return true
		}
	}
	return false
}

// feishuCardEvent maps a card button click (card.action.trigger) to an
// action event. The button value carries the action and the delivery; the
// clicker is identified by open_id, like private-chat senders.
func feishuCardEvent(ev *callback.CardActionTriggerEvent) (InboundEvent, bool) {
	if ev == nil || ev.EventV2Base == nil || ev.EventV2Base.Header == nil || ev.Event == nil || ev.Event.Action == nil || ev.Event.Operator == nil {
		return InboundEvent{}, false
	}
	action, _ := ev.Event.Action.Value["af_action"].(string)
	delivery, _ := ev.Event.Action.Value["af_delivery"].(string)
	option := strings.TrimSpace(ev.Event.Action.Option)
	switch {
	case DraftAction(action):
		// Draft confirmation cards carry the draft ID instead.
		delivery, _ = ev.Event.Action.Value["af_draft"].(string)
	case ClarifyAction(action):
		// Clarification cards carry the session and the button reference.
		delivery, _ = ev.Event.Action.Value["af_clarify"].(string)
		option, _ = ev.Event.Action.Value["af_ref"].(string)
	}
	if (DraftSelectAction(action) || ClarifyAction(action)) && strings.TrimSpace(option) == "" {
		return InboundEvent{}, false
	}

	if !pushCardKey(action) || delivery == "" || ev.Event.Operator.OpenID == "" || ev.EventV2Base.Header.EventID == "" {
		return InboundEvent{}, false
	}
	out := InboundEvent{
		Channel:    ChannelFeishu,
		AppID:      ev.EventV2Base.Header.AppID,
		TenantKey:  ev.EventV2Base.Header.TenantKey,
		EventID:    ev.EventV2Base.Header.EventID,
		MessageID:  "card:" + ev.EventV2Base.Header.EventID,
		SenderID:   ev.Event.Operator.OpenID,
		ChatID:     ev.Event.Operator.OpenID,
		ChatType:   "single",
		MsgType:    "card_action",
		Action:     action,
		DeliveryID: delivery,
		Option:     strings.TrimSpace(option),
	}
	if action == "answer" {
		// The node question card submits its answer input as a form.
		out.Text, _ = ev.Event.Action.FormValue[PushCardAnswerField].(string)
		out.Text = strings.TrimSpace(out.Text)
	}
	if ev.Event.Context != nil {
		// Replies to a click are threaded under the card.
		out.Route.MessageID = ev.Event.Context.OpenMessageID
		out.CardChatID = ev.Event.Context.OpenChatID
	}
	if out.TenantKey == "" && ev.Event.Operator.TenantKey != nil {
		out.TenantKey = *ev.Event.Operator.TenantKey
	}
	return out, true
}

func feishuToast(kind, text string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: kind, Content: text}}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type feishuConn struct {
	channel   *feishuChannel
	bot       domain.NotificationBot
	api       *lark.Client
	ws        *larkws.Client
	cancel    context.CancelFunc
	done      chan struct{}
	err       error
	closeOnce sync.Once
}

func (c *feishuConn) Done() <-chan struct{} { return c.done }

// Close stops the client and waits for its workers to finish.
func (c *feishuConn) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		waitCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = c.ws.CloseAndWait(waitCtx)
	})
	return nil
}

// Reply answers the original message with the persisted key as uuid.
func (c *feishuConn) Reply(ctx context.Context, msg OutboundMessage) SendResult {
	if msg.Route.MessageID == "" {
		return SendResult{Outcome: Rejected, Code: "route_missing", SafeDetail: "缺少飞书原消息标识，无法在原会话回复"}
	}
	msgType, content := "text", []byte(nil)
	if msg.Card != nil {
		// Write confirmations go out as a card with buttons.
		msgType, content = "interactive", feishuCardJSON(msg.Card)
	} else {
		content, _ = json.Marshal(map[string]string{"text": msg.Text})
	}
	body := larkim.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(string(content))
	if msg.SendKey != "" {
		body = body.Uuid(msg.SendKey)
	}
	req := larkim.NewReplyMessageReqBuilder().MessageId(msg.Route.MessageID).Body(body.Build()).Build()
	resp, err := c.api.Im.V1.Message.Reply(ctx, req)
	if err != nil {
		return classifyFeishuTransport(err)
	}
	if !resp.Success() {
		return classifyFeishuCode(resp.Code)
	}
	out := SendResult{Outcome: Delivered}
	if resp.Data != nil {
		out.PlatformMessageID = deref(resp.Data.MessageId)
	}
	return out
}

// classifyFeishuTransport: a dial failure means nothing was sent (the SDK
// already retried once); any other transport error may follow a delivered
// request and is unknown — safe to retry with the same uuid.
func classifyFeishuTransport(err error) SendResult {
	var dial *larkcore.DialFailedError
	if errors.As(err, &dial) {
		return SendResult{Outcome: NotDelivered, Code: "dial_failed", Retryable: true, SafeDetail: "无法连接飞书开放平台"}
	}
	return SendResult{Outcome: Unknown, Code: "transport", SafeDetail: "飞书回复请求未返回结果"}
}

// feishuRetryable lists platform codes that mean "not sent, try again":
// request rate limits and an invalid/expired tenant access token (the next
// attempt fetches a new token). Unknown codes stop automatic retries.
var feishuRetryable = map[int]bool{
	99991400: true, // request frequency limit
	99991661: true, // missing access token
	99991663: true, // invalid tenant access token
	99991664: true, // invalid app access token
	99991668: true, // invalid user access token
}

func classifyFeishuCode(code int) SendResult {
	detail := "飞书拒绝了这次回复（错误码 " + strconv.Itoa(code) + "）"
	if feishuRetryable[code] {
		return SendResult{Outcome: NotDelivered, Code: strconv.Itoa(code), Retryable: true, SafeDetail: detail}
	}
	if strings.HasPrefix(strconv.Itoa(code), "5") && code < 600 {
		return SendResult{Outcome: Unknown, Code: strconv.Itoa(code), SafeDetail: detail}
	}
	return SendResult{Outcome: Rejected, Code: strconv.Itoa(code), SafeDetail: detail}
}

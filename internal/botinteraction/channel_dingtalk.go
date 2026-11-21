package botinteraction

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

// dingtalkChannel connects a DingTalk enterprise internal application over
// the Stream protocol and replies through the session webhook of each
// message. Only the data types of open-dingtalk/dingtalk-stream-sdk-go
// v0.9.1 are used (payload frames, chatbot callback model): its StreamClient
// can crash the host process after a pong timeout, cannot be cancelled while
// dialing and keeps reconnecting after Close (scripts/im-sdk-probe/README.md).
// This adapter owns the connection instead:
//
//   - one reader goroutine; writes are serialized; nothing ever sends on a
//     channel that another goroutine closes;
//   - WebSocket pings every keepAlive, and the read deadline closes a
//     connection that stops answering;
//   - dialing and every HTTP call honor the context and have timeouts;
//   - no reconnect inside the adapter: the service reconnects with backoff
//     and re-reads the credential, as for the other channels;
//   - a bot callback is acknowledged only after persistence, bounded by
//     ackTimeout; storage errors answer 500 so DingTalk redelivers.
type dingtalkChannel struct {
	// api is the open API origin; tests point it at a local server.
	api  string
	http *http.Client
	// allowInsecureStream accepts ws:// endpoints (tests only).
	allowInsecureStream bool
	keepAlive           time.Duration
	pongWait            time.Duration
	ackTimeout          time.Duration
	handshakeTimeout    time.Duration
	maxInFlight         int
	// webhookAllowed decides whether a session webhook from an event may be
	// used. Production accepts only the official HTTPS endpoint.
	webhookAllowed func(*url.URL) bool
}

const (
	dingtalkDefaultAPI = "https://api.dingtalk.com"
	dingtalkUserAgent  = "loopforge-dingtalk-stream/1"
	// dingtalkMaxResponse bounds every response body read from DingTalk.
	dingtalkMaxResponse = 64 << 10
)

func newDingTalkChannel() *dingtalkChannel {
	return &dingtalkChannel{
		api:              dingtalkDefaultAPI,
		http:             &http.Client{Timeout: sendTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		keepAlive:        60 * time.Second,
		pongWait:         10 * time.Second,
		ackTimeout:       2500 * time.Millisecond,
		handshakeTimeout: 10 * time.Second,
		maxInFlight:      8,
		webhookAllowed:   officialDingTalkWebhook,
	}
}

func (d *dingtalkChannel) Kind() string { return ChannelDingTalk }

// officialDingTalkWebhook accepts only the official session reply endpoint.
func officialDingTalkWebhook(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Host == "oapi.dingtalk.com" && u.Path == "/robot/sendBySession" && u.User == nil && u.Fragment == ""
}

// postJSON sends one JSON request without following redirects and reads at
// most dingtalkMaxResponse bytes of the answer.
func (d *dingtalkChannel) postJSON(ctx context.Context, target string, body any) (int, []byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", dingtalkUserAgent)
	resp, err := d.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, dingtalkMaxResponse))
	return resp.StatusCode, data, err
}

// Verify obtains an application access token, which proves the Client ID and
// Client Secret. The enterprise is confirmed from trusted events against the
// CorpId the administrator entered.
func (d *dingtalkChannel) Verify(ctx context.Context, bot domain.NotificationBot, cred Credential) (Verification, error) {
	status, data, err := d.postJSON(ctx, d.api+"/v1.0/oauth2/accessToken", map[string]string{"appKey": bot.BotID, "appSecret": cred.Secret})
	if err != nil {
		return Verification{}, errors.New("钉钉开放平台暂不可用，请稍后重试")
	}
	var out struct {
		AccessToken string `json:"accessToken"`
		Code        string `json:"code"`
	}
	_ = json.Unmarshal(data, &out)
	if status != http.StatusOK || out.AccessToken == "" {
		code := out.Code
		if code == "" {
			code = strconv.Itoa(status)
		}
		return Verification{}, fmt.Errorf("钉钉拒绝了 Client ID 或 Client Secret（%s）", truncate(code, 64))
	}
	return Verification{}, nil
}

// openConnection registers the subscriptions and returns the Stream endpoint
// and one-time ticket.
func (d *dingtalkChannel) openConnection(ctx context.Context, bot domain.NotificationBot, cred Credential) (*payload.ConnectionEndpointResponse, error) {
	reqBody := payload.ConnectionEndpointRequest{
		ClientId:     bot.BotID,
		ClientSecret: cred.Secret,
		UserAgent:    dingtalkUserAgent,
		Subscriptions: []*payload.SubscriptionModel{
			{Type: "SYSTEM", Topic: "ping"},
			{Type: "SYSTEM", Topic: "disconnect"},
			{Type: "CALLBACK", Topic: payload.BotMessageCallbackTopic},
			{Type: "CALLBACK", Topic: payload.CardInstanceCallbackTopic},
		},
	}
	status, data, err := d.postJSON(ctx, d.api+"/v1.0/gateway/connections/open", reqBody)
	if err != nil {
		return nil, errors.New("钉钉 Stream 接入点暂不可用")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("钉钉拒绝建立 Stream 连接（HTTP %d）", status)
	}
	endpoint := &payload.ConnectionEndpointResponse{}
	if err := json.Unmarshal(data, endpoint); err != nil || endpoint.Valid() != nil {
		return nil, errors.New("钉钉 Stream 接入点响应无效")
	}
	u, err := url.Parse(endpoint.Endpoint)
	if err != nil || (u.Scheme != "wss" && !(d.allowInsecureStream && u.Scheme == "ws")) || u.Host == "" {
		return nil, errors.New("钉钉 Stream 接入点地址无效")
	}
	return endpoint, nil
}

// Connect opens the Stream connection and returns once it is established.
func (d *dingtalkChannel) Connect(ctx context.Context, bot domain.NotificationBot, cred Credential, sink InboundSink) (Conn, error) {
	endpoint, err := d.openConnection(ctx, bot, cred)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint.Endpoint)
	q := u.Query()
	q.Set("ticket", endpoint.Ticket)
	u.RawQuery = q.Encode()
	dialer := &websocket.Dialer{HandshakeTimeout: d.handshakeTimeout, Proxy: http.ProxyFromEnvironment}
	dialCtx, cancelDial := context.WithTimeout(ctx, d.handshakeTimeout)
	ws, resp, err := dialer.DialContext(dialCtx, u.String(), nil)
	cancelDial()
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, errors.New("钉钉 Stream 连接建立失败")
	}
	runCtx, cancel := context.WithCancel(ctx)
	c := &dingtalkConn{channel: d, bot: bot, ws: ws, sink: sink, ctx: runCtx, cancel: cancel, done: make(chan struct{}), slots: make(chan struct{}, max(1, d.maxInFlight))}
	ws.SetReadLimit(1 << 20)
	c.extendRead()
	ws.SetPongHandler(func(string) error { c.extendRead(); return nil })
	go c.keepalive()
	go c.readLoop()
	return c, nil
}

type dingtalkConn struct {
	channel *dingtalkChannel
	bot     domain.NotificationBot
	ws      *websocket.Conn
	sink    InboundSink
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	writeMu  sync.Mutex
	handlers sync.WaitGroup
	slots    chan struct{}
	stopOnce sync.Once
}

func (c *dingtalkConn) Done() <-chan struct{} { return c.done }

// Close stops the connection and waits for the reader and in-flight
// acknowledgements to finish.
func (c *dingtalkConn) Close() error {
	c.stop()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
	}
	return nil
}

// stop cancels the context and closes the socket, which ends the reader.
func (c *dingtalkConn) stop() {
	c.stopOnce.Do(func() {
		c.cancel()
		_ = c.ws.Close()
	})
}

func (c *dingtalkConn) extendRead() {
	_ = c.ws.SetReadDeadline(time.Now().Add(c.channel.keepAlive + c.channel.pongWait))
}

// keepalive pings the server and closes the socket when the context ends.
func (c *dingtalkConn) keepalive() {
	ticker := time.NewTicker(c.channel.keepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			c.stop()
			return
		case <-ticker.C:
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				c.stop()
				return
			}
		}
	}
}

// readLoop is the only reader. When it ends the connection is stopped, the
// handlers drain (their writes fail on the closed socket) and Done closes.
func (c *dingtalkConn) readLoop() {
	defer func() {
		c.stop()
		c.handlers.Wait()
		close(c.done)
	}()
	for {
		kind, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		c.extendRead()
		if kind != websocket.TextMessage {
			continue
		}
		if !c.handleFrame(data) {
			return
		}
	}
}

// handleFrame routes one data frame; false ends the connection.
func (c *dingtalkConn) handleFrame(data []byte) bool {
	frame, err := payload.DecodeDataFrame(data)
	if err != nil || frame == nil || frame.Headers == nil {
		log.Printf("dingtalk stream frame ignored: undecodable")
		return true
	}
	switch {
	case frame.Type == "SYSTEM" && frame.GetTopic() == "ping":
		pong := payload.NewDataFrameAckPong(frame.GetMessageId())
		pong.Data = frame.Data
		return c.write(pong) == nil
	case frame.Type == "SYSTEM" && frame.GetTopic() == "disconnect":
		// The server is about to close this connection; reconnect now.
		return false
	case frame.Type == "CALLBACK" && (frame.GetTopic() == payload.BotMessageCallbackTopic || frame.GetTopic() == payload.CardInstanceCallbackTopic):
		select {
		case c.slots <- struct{}{}:
		case <-c.ctx.Done():
			return false
		}
		c.handlers.Add(1)
		go func() {
			defer func() { <-c.slots; c.handlers.Done() }()
			if frame.GetTopic() == payload.CardInstanceCallbackTopic {
				c.write(c.handleCardCallback(frame))
				return
			}
			c.write(c.handleCallback(frame))
		}()
		return true
	default:
		resp := payload.NewDataFrameResponse(payload.DataFrameResponseStatusCodeKHandlerNotFound)
		resp.SetHeader(payload.DataFrameHeaderKMessageId, frame.GetMessageId())
		resp.SetHeader(payload.DataFrameHeaderKContentType, payload.DataFrameContentTypeKJson)
		return c.write(resp) == nil
	}
}

// handleCallback persists a bot message and builds its acknowledgement.
func (c *dingtalkConn) handleCallback(frame *payload.DataFrame) *payload.DataFrameResponse {
	resp := payload.NewSuccessDataFrameResponse()
	ev, reason := dingtalkEvent(c.bot, frame.Data, frame.GetMessageId(), c.channel.webhookAllowed)
	if reason != "" {
		log.Printf("dingtalk inbound ignored reason=%s", reason)
	} else if err := c.acknowledge(ev); err != nil {
		resp = payload.NewErrorDataFrameResponse(errors.New("persist_failed"))
	}
	resp.SetHeader(payload.DataFrameHeaderKMessageId, frame.GetMessageId())
	resp.SetHeader(payload.DataFrameHeaderKContentType, payload.DataFrameContentTypeKJson)
	return resp
}

// handleCardCallback persists an action card click. DingTalk expects an
// answer within two seconds; the card status line is updated in place and
// the result follows as a private message.
func (c *dingtalkConn) handleCardCallback(frame *payload.DataFrame) *payload.DataFrameResponse {
	status := "已收到，处理结果会以消息发给你"
	params := map[string]string{}
	ev, reason := dingtalkCardEvent(c.bot, frame.Data, frame.GetMessageId())
	if reason != "" {
		log.Printf("dingtalk card callback ignored reason=%s", reason)
		status = "这张卡片已失效，请到平台查看运行状态"
	} else if ack, err := c.acknowledgeAck(ev); err != nil {
		status = "暂时无法处理，请稍后再点一次"
	} else if ack.Ignored != "" && ack.Toast != "" {
		// Refused (for example a group member without a bound account):
		// keep the buttons for the others.
		status = ack.Toast
	} else {
		// Taken: hide the buttons so the card cannot be clicked again.
		status = "已提交「" + pushCardActionLabel(ev.Action) + "」，处理结果会以消息发给你"
		if ack.Card != nil && ack.Card.Status != "" {
			status = ack.Card.Status
		} else if ack.Toast != "" && ack.Accepted {
			status = ack.Toast
		}
		for _, key := range PushCardKeys {
			params["show_"+key] = "false"
		}
	}
	params["status"] = status
	resp := payload.NewSuccessDataFrameResponse()
	_ = resp.SetJson(map[string]any{"response": map[string]any{
		"cardData":          map[string]any{"cardParamMap": params},
		"cardUpdateOptions": map[string]bool{"updateCardDataByKey": true},
	}})
	resp.SetHeader(payload.DataFrameHeaderKMessageId, frame.GetMessageId())
	resp.SetHeader(payload.DataFrameHeaderKContentType, payload.DataFrameContentTypeKJson)
	return resp
}

// dingtalkCardEvent maps a card callback to an action event. The template
// buttons pass {"action": "<key>"} as callback parameters; the instance ID
// names the delivery.
func dingtalkCardEvent(bot domain.NotificationBot, raw, frameID string) (InboundEvent, string) {
	var req struct {
		Type       string `json:"type"`
		OutTrackID string `json:"outTrackId"`
		CorpID     string `json:"corpId"`
		UserID     string `json:"userId"`
		Content    string `json:"content"`
	}
	if json.Unmarshal([]byte(raw), &req) != nil {
		return InboundEvent{}, "undecodable"
	}
	var content struct {
		CardPrivateData struct {
			ActionIDs []string       `json:"actionIds"`
			Params    map[string]any `json:"params"`
		} `json:"cardPrivateData"`
	}
	_ = json.Unmarshal([]byte(req.Content), &content)
	action, _ := content.CardPrivateData.Params["action"].(string)
	if action == "" && len(content.CardPrivateData.ActionIDs) > 0 {
		action = content.CardPrivateData.ActionIDs[0]
	}
	action = strings.TrimSpace(action)
	if !pushCardKey(action) {
		return InboundEvent{}, "unknown_action"
	}
	if req.OutTrackID == "" || req.UserID == "" || frameID == "" {
		return InboundEvent{}, "missing_identity"
	}
	return InboundEvent{
		Channel:    ChannelDingTalk,
		AppID:      bot.BotID, // callbacks arrive only on this application's connection
		TenantKey:  req.CorpID,
		EventID:    frameID,
		MessageID:  "card:" + frameID,
		SenderID:   req.UserID,
		ChatID:     req.UserID,
		ChatType:   "single",
		MsgType:    "card_action",
		Action:     action,
		DeliveryID: dingtalkCardDelivery(req.OutTrackID),
	}, ""
}

// acknowledge runs the sink with the ack budget. Repository calls take no
// context, so a slow database answers with a failure at the deadline; the
// redelivered message is de-duplicated by its msgId.
func (c *dingtalkConn) acknowledge(ev InboundEvent) error {
	_, err := c.acknowledgeAck(ev)
	return err
}

// acknowledgeAck is acknowledge returning the sink's answer (card clicks).
func (c *dingtalkConn) acknowledgeAck(ev InboundEvent) (InboundAck, error) {
	ackCtx, cancel := context.WithTimeout(c.ctx, c.channel.ackTimeout)
	defer cancel()
	type result struct {
		ack InboundAck
		err error
	}
	done := make(chan result, 1)
	go func() {
		ack, err := c.sink(ackCtx, ev)
		done <- result{ack, err}
	}()
	select {
	case r := <-done:
		return r.ack, r.err
	case <-ackCtx.Done():
		return InboundAck{}, errDingTalkAckTimeout
	}
}

var errDingTalkAckTimeout = errors.New("钉钉事件入库超时，等待平台重投")

func (c *dingtalkConn) write(resp *payload.DataFrameResponse) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.ws.WriteJSON(resp)
}

// dingtalkCallback is the bot callback payload. robotCode is not part of the
// SDK model; when present it must name this application.
type dingtalkCallback struct {
	chatbot.BotCallbackDataModel
	RobotCode string `json:"robotCode"`
}

// dingtalkEvent maps a bot callback to the channel-neutral event. A non-empty
// reason means the callback is acknowledged and dropped.
func dingtalkEvent(bot domain.NotificationBot, raw, frameID string, webhookAllowed func(*url.URL) bool) (InboundEvent, string) {
	var m dingtalkCallback
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return InboundEvent{}, "undecodable"
	}
	if m.SenderStaffId == "" {
		// Without the verified staff ID there is no stable identity to bind.
		return InboundEvent{}, "missing_staff_id"
	}
	if m.SenderCorpId != "" && m.ChatbotCorpId != "" && m.SenderCorpId != m.ChatbotCorpId {
		return InboundEvent{}, "sender_corp_mismatch"
	}
	appID := bot.BotID
	if m.RobotCode != "" {
		appID = m.RobotCode
	}
	ev := InboundEvent{
		Channel:   ChannelDingTalk,
		AppID:     appID,
		TenantKey: m.ChatbotCorpId,
		EventID:   frameID,
		MessageID: m.MsgId,
		SenderID:  m.SenderStaffId,
		ChatID:    m.ConversationId,
		MsgType:   m.Msgtype,
	}
	switch m.ConversationType {
	case "1":
		ev.ChatType = "single"
	case "2":
		// DingTalk delivers group messages to the robot only when it is @;
		// isInAtList confirms it when present.
		ev.ChatType = "group"
		ev.Mentioned = m.IsInAtList || !strings.Contains(raw, `"isInAtList"`)
	default:
		ev.ChatType = m.ConversationType
	}
	if m.Msgtype == "text" {
		ev.Text = strings.TrimSpace(m.Text.Content)
	}
	if m.SessionWebhook != "" {
		u, err := url.Parse(m.SessionWebhook)
		if err != nil || webhookAllowed == nil || !webhookAllowed(u) {
			return InboundEvent{}, "untrusted_webhook"
		}
		ev.Route.Webhook = m.SessionWebhook
		if m.SessionWebhookExpiredTime > 0 {
			// sessionWebhookExpiredTime is epoch milliseconds.
			ev.Route.ExpiresAt = time.UnixMilli(m.SessionWebhookExpiredTime).UTC()
		}
	}
	return ev, ""
}

// Reply answers in the original conversation through its session webhook.
func (c *dingtalkConn) Reply(ctx context.Context, msg OutboundMessage) SendResult {
	return c.channel.replyWebhook(ctx, msg)
}

func (d *dingtalkChannel) replyWebhook(ctx context.Context, msg OutboundMessage) SendResult {
	if msg.Route.Webhook == "" {
		return SendResult{Outcome: Rejected, Code: "route_missing", SafeDetail: "缺少钉钉会话回复地址，无法在原会话回复"}
	}
	u, err := url.Parse(msg.Route.Webhook)
	if err != nil || d.webhookAllowed == nil || !d.webhookAllowed(u) {
		return SendResult{Outcome: Rejected, Code: "route_untrusted", SafeDetail: "钉钉会话回复地址无效"}
	}
	if !msg.Route.ExpiresAt.IsZero() && !time.Now().Add(sendTimeout).Before(msg.Route.ExpiresAt) {
		return SendResult{Outcome: Rejected, Code: "route_expired", SafeDetail: "会话回复授权已过期"}
	}
	status, data, err := d.postJSON(ctx, u.String(), map[string]any{"msgtype": "text", "text": map[string]string{"content": msg.Text}})
	if err != nil {
		return classifyDingTalkTransport(err)
	}
	return classifyDingTalkResponse(status, data)
}

// classifyDingTalkTransport: a failed dial or DNS lookup sent nothing; any
// other transport error may follow a delivered request. DingTalk session
// replies have no idempotency key, so unknown stops at reply_unknown.
func classifyDingTalkTransport(err error) SendResult {
	var op *net.OpError
	var dns *net.DNSError
	if (errors.As(err, &op) && op.Op == "dial") || errors.As(err, &dns) {
		return SendResult{Outcome: NotDelivered, Code: "dial_failed", Retryable: true, SafeDetail: "无法连接钉钉开放平台"}
	}
	return SendResult{Outcome: Unknown, Code: "transport", SafeDetail: "钉钉回复请求未返回结果"}
}

// dingtalkRetryable lists errcodes meaning "not sent, try again".
var dingtalkRetryable = map[int]bool{
	130101: true, // sending too fast
}

func classifyDingTalkResponse(status int, data []byte) SendResult {
	switch {
	case status == http.StatusTooManyRequests:
		return SendResult{Outcome: NotDelivered, Code: "429", Retryable: true, SafeDetail: "钉钉限流，稍后重试"}
	case status >= 500:
		return SendResult{Outcome: Unknown, Code: strconv.Itoa(status), SafeDetail: "钉钉返回服务端错误，送达结果未知"}
	case status != http.StatusOK:
		return SendResult{Outcome: Rejected, Code: strconv.Itoa(status), SafeDetail: "钉钉拒绝了这次回复（HTTP " + strconv.Itoa(status) + "）"}
	}
	var out struct {
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(data, &out) != nil || out.ErrCode == nil {
		return SendResult{Outcome: Unknown, Code: "unparsed", SafeDetail: "钉钉回复响应无法识别，送达结果未知"}
	}
	code := *out.ErrCode
	if code == 0 {
		return SendResult{Outcome: Delivered}
	}
	detail := "钉钉拒绝了这次回复（错误码 " + strconv.Itoa(code) + "）"
	if dingtalkRetryable[code] {
		return SendResult{Outcome: NotDelivered, Code: strconv.Itoa(code), Retryable: true, SafeDetail: detail}
	}
	if code == -1 {
		// "System busy" does not say whether the message went out.
		return SendResult{Outcome: Unknown, Code: "-1", SafeDetail: "钉钉系统繁忙，送达结果未知"}
	}
	return SendResult{Outcome: Rejected, Code: strconv.Itoa(code), SafeDetail: detail}
}

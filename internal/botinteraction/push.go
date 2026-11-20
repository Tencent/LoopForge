package botinteraction

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Push sends one proactive message through the channel's application API.
// It needs no receiving connection, so notification-only configurations
// (interaction disabled) can use it. msg.ChatID is the platform target: the
// WeCom chat ID, the Feishu open_id or the DingTalk staff ID of a bound
// private-chat sender.
func (s *Service) Push(ctx context.Context, bot domain.NotificationBot, msg OutboundMessage) SendResult {
	channel, err := s.Channel(bot)
	if err != nil {
		return SendResult{Outcome: NotDelivered, Code: ErrChannelNotReady.Error(), SafeDetail: "该渠道的主动发送未开放"}
	}
	sender, ok := channel.(PushSender)
	if !ok {
		return SendResult{Outcome: NotDelivered, Code: ErrChannelNotReady.Error(), SafeDetail: "该渠道的主动发送未开放"}
	}
	secret, err := s.Store.BotCredential(bot.ID)
	if err != nil || secret == "" {
		return SendResult{Outcome: NotDelivered, Code: "credential_unavailable", SafeDetail: "机器人凭据不可用，请重新填写 Secret"}
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	return sender.Push(ctx, bot, Credential{Secret: secret}, msg)
}

// PushIdempotent reports whether the channel de-duplicates proactive sends
// carrying the same key (Feishu uuid, one hour), so an unknown outcome may
// be retried with that key inside the window.
func PushIdempotent(bot domain.NotificationBot) bool {
	return bot.ChannelKind() == domain.BotChannelFeishu
}

// Push creates a message (private to an open_id, or to a group chat_id) with
// the persisted key as uuid; Feishu drops a repeated uuid within one hour.
func (f *feishuChannel) Push(ctx context.Context, bot domain.NotificationBot, cred Credential, msg OutboundMessage) SendResult {
	if strings.TrimSpace(msg.ChatID) == "" {
		return SendResult{Outcome: Rejected, Code: "target_missing", SafeDetail: "缺少飞书接收成员"}
	}
	msgType, content := "text", []byte(nil)
	if msg.Card != nil {
		msgType, content = "interactive", feishuCardJSON(msg.Card)
	} else {
		content, _ = json.Marshal(map[string]string{"text": msg.Text})
	}
	body := larkim.NewCreateMessageReqBodyBuilder().ReceiveId(msg.ChatID).MsgType(msgType).Content(string(content))
	if msg.SendKey != "" {
		body = body.Uuid(msg.SendKey)
	}
	// Private messages go to the member's open_id; group messages (and
	// conversation pushes) to the chat_id.
	receiveType := "open_id"
	if msg.ChatType == "group" {
		receiveType = "chat_id"
	}
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType(receiveType).Body(body.Build()).Build()
	resp, err := f.client(bot, cred).Im.V1.Message.Create(ctx, req)
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

// dingtalkToken caches one application access token per credential version.
type dingtalkToken struct {
	key     string
	value   string
	expires time.Time
}

var dingtalkTokens = struct {
	sync.Mutex
	m map[string]dingtalkToken
}{m: map[string]dingtalkToken{}}

func dingtalkTokenKey(bot domain.NotificationBot) string {
	return bot.ID + "|" + bot.BotID + "|" + strconv.FormatInt(bot.CredentialVersion, 10)
}

// accessToken returns a cached or new application token. A failure means
// nothing was sent: rejected credentials are Rejected, anything else is
// NotDelivered and retryable.
func (d *dingtalkChannel) accessToken(ctx context.Context, bot domain.NotificationBot, cred Credential) (string, *SendResult) {
	key := dingtalkTokenKey(bot)
	dingtalkTokens.Lock()
	cached, ok := dingtalkTokens.m[bot.ID]
	dingtalkTokens.Unlock()
	if ok && cached.key == key && time.Now().Before(cached.expires) {
		return cached.value, nil
	}
	status, data, err := d.postJSON(ctx, d.api+"/v1.0/oauth2/accessToken", map[string]string{"appKey": bot.BotID, "appSecret": cred.Secret})
	if err != nil {
		return "", &SendResult{Outcome: NotDelivered, Code: "token_unavailable", Retryable: true, SafeDetail: "无法获取钉钉访问凭证，稍后重试"}
	}
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
		Code        string `json:"code"`
	}
	_ = json.Unmarshal(data, &out)
	if status != http.StatusOK || out.AccessToken == "" {
		if status >= 500 || status == http.StatusTooManyRequests {
			return "", &SendResult{Outcome: NotDelivered, Code: "token_unavailable", Retryable: true, SafeDetail: "无法获取钉钉访问凭证，稍后重试"}
		}
		return "", &SendResult{Outcome: Rejected, Code: "credential_invalid", SafeDetail: "钉钉拒绝了 Client ID 或 Client Secret"}
	}
	ttl := time.Duration(out.ExpireIn) * time.Second
	if ttl <= 0 || ttl > 2*time.Hour {
		ttl = 2 * time.Hour
	}
	dingtalkTokens.Lock()
	dingtalkTokens.m[bot.ID] = dingtalkToken{key: key, value: out.AccessToken, expires: time.Now().Add(ttl - 5*time.Minute)}
	dingtalkTokens.Unlock()
	return out.AccessToken, nil
}

func forgetDingTalkToken(bot domain.NotificationBot) {
	dingtalkTokens.Lock()
	delete(dingtalkTokens.m, bot.ID)
	dingtalkTokens.Unlock()
}

// Push sends a private message to a staff ID through the robot batch API, or
// a group message to an open conversation ID (ChatType "group"); robotCode
// is the Client ID of the enterprise internal application. The
// API has no idempotency key: an unknown outcome is never retried
// automatically.
func (d *dingtalkChannel) Push(ctx context.Context, bot domain.NotificationBot, cred Credential, msg OutboundMessage) SendResult {
	if strings.TrimSpace(msg.ChatID) == "" {
		return SendResult{Outcome: Rejected, Code: "target_missing", SafeDetail: "缺少钉钉接收成员"}
	}
	token, failed := d.accessToken(ctx, bot, cred)
	if failed != nil {
		return *failed
	}
	if msg.Card != nil && strings.TrimSpace(bot.CardTemplateID) != "" {
		out := d.deliverCard(ctx, bot, token, msg)
		if out.Code == "token_invalid" {
			forgetDingTalkToken(bot)
		}
		return out
	}
	param, _ := json.Marshal(map[string]string{"content": msg.Text})
	endpoint := "/v1.0/robot/oToMessages/batchSend"
	raw, _ := json.Marshal(map[string]any{"robotCode": bot.BotID, "userIds": []string{msg.ChatID}, "msgKey": "sampleText", "msgParam": string(param)})
	if msg.ChatType == "group" {
		// Group chats: the robot group message API with the open
		// conversation ID of the group.
		endpoint = "/v1.0/robot/groupMessages/send"
		raw, _ = json.Marshal(map[string]any{"robotCode": bot.BotID, "openConversationId": msg.ChatID, "msgKey": "sampleText", "msgParam": string(param)})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.api+endpoint, bytes.NewReader(raw))
	if err != nil {
		return SendResult{Outcome: NotDelivered, Code: "request", SafeDetail: "钉钉请求无法构造"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", dingtalkUserAgent)
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := d.http.Do(req)
	if err != nil {
		return classifyDingTalkTransport(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, dingtalkMaxResponse))
	out := classifyDingTalkPush(resp.StatusCode, data, msg.ChatID)
	if out.Code == "token_invalid" {
		forgetDingTalkToken(bot)
	}
	return out
}

// classifyDingTalkPush maps the batchSend answer. The staff lists report
// per-recipient refusals inside an HTTP 200 answer.
func classifyDingTalkPush(status int, data []byte, staffID string) SendResult {
	var out struct {
		ProcessQueryKey           string   `json:"processQueryKey"`
		InvalidStaffIDList        []string `json:"invalidStaffIdList"`
		FlowControlledStaffIDList []string `json:"flowControlledStaffIdList"`
		Code                      string   `json:"code"`
		Message                   string   `json:"message"`
	}
	_ = json.Unmarshal(data, &out)
	switch {
	case status == http.StatusTooManyRequests:
		return SendResult{Outcome: NotDelivered, Code: "429", Retryable: true, SafeDetail: "钉钉限流，稍后重试"}
	case status >= 500:
		return SendResult{Outcome: Unknown, Code: strconv.Itoa(status), SafeDetail: "钉钉返回服务端错误，送达结果未知"}
	case status == http.StatusUnauthorized || (status >= 400 && strings.Contains(strings.ToLower(out.Code), "accesstoken")):
		return SendResult{Outcome: NotDelivered, Code: "token_invalid", Retryable: true, SafeDetail: "钉钉访问凭证已失效，稍后重试"}
	case status != http.StatusOK:
		code := out.Code
		if code == "" {
			code = strconv.Itoa(status)
		}
		return SendResult{Outcome: Rejected, Code: truncate(code, 64), SafeDetail: "钉钉拒绝了这次发送（" + truncate(code, 64) + "）"}
	}
	if slices.Contains(out.InvalidStaffIDList, staffID) {
		return SendResult{Outcome: Rejected, Code: "invalid_staff", SafeDetail: "钉钉不接受该成员：成员不在应用可见范围内或已离职"}
	}
	if slices.Contains(out.FlowControlledStaffIDList, staffID) {
		return SendResult{Outcome: NotDelivered, Code: "flow_controlled", Retryable: true, SafeDetail: "钉钉对该成员限流，稍后重试"}
	}
	if out.ProcessQueryKey == "" {
		return SendResult{Outcome: Unknown, Code: "unparsed", SafeDetail: "钉钉发送响应无法识别，送达结果未知"}
	}
	return SendResult{Outcome: Delivered, PlatformMessageID: truncate(out.ProcessQueryKey, 191)}
}

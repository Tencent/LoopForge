package botinteraction

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Action cards of Feishu and DingTalk (proactive notifications of waiting or
// failed nodes). The buttons carry the delivery ID and the action key; a
// click comes back on the receiving connection as an InboundEvent with
// Action and DeliveryID set and runs through the same checks as WeCom card
// clicks (bound sender, project permission, node still in the notified
// state, action still offered). The result is sent to the clicker as a
// private message.

// PushCard is the channel-neutral content of an action card.
type PushCard struct {
	DeliveryID string
	Title      string
	Tag        string // 待人工确认 or 执行失败
	Reason     string
	Required   string
	Rows       [][2]string
	Actions    []PushCardAction
	DetailsURL string
}

// PushCardAction is one button.
type PushCardAction struct {
	Key    string
	Text   string
	Danger bool
}

// PushCardKeys are the action keys a card button may carry.
var PushCardKeys = []string{"approve", "retry", "reject", "pass", "resume", "decline"}

// PushCardFor builds the action card of a waiting or failed node delivery,
// or nil when the node no longer offers an action.
func (s *Service) PushCardFor(item domain.NotificationDelivery, content, detailsURL string) *PushCard {
	actions := ActionsForDelivery(s.Store, item)
	if len(actions) == 0 {
		return nil
	}
	run, ok := s.Store.RunState(item.RunID)
	if !ok {
		return nil
	}
	reason, required := cardReason(run, item, content)
	issue := notificationCardIssue(content)
	title := issue
	if title == "未关联需求" {
		title = run.WorkflowSnapshot.Name
	}
	card := &PushCard{DeliveryID: item.ID, Title: cardText(title, 60), Tag: map[string]string{"waiting": "待人工确认", "failed": "执行失败"}[item.EventType], Reason: cardText(reason, 300), Required: cardText(required, 120), DetailsURL: detailsURL}
	card.Rows = [][2]string{{"需求", cardText(issue, 60)}, {"节点", cardText(item.SourceName, 60)}, {"SOP", cardText(run.WorkflowSnapshot.Name, 60)}}
	buttons, _ := notificationCardActions(actions)
	for _, b := range buttons {
		key, _ := b["key"].(string)
		text, _ := b["text"].(string)
		style, _ := b["style"].(int)
		if key == "reject" {
			if target := cardReworkTarget(run, item.NodeID); target != "" {
				card.Rows = append(card.Rows, [2]string{"返工到", cardText(target, 60)})
			}
		}
		card.Actions = append(card.Actions, PushCardAction{Key: key, Text: text, Danger: style == 4})
	}
	return card
}

// pushCardKey reports whether key is a card action.
func pushCardKey(key string) bool {
	for _, k := range PushCardKeys {
		if k == key {
			return true
		}
	}
	return false
}

// PlainText is the card as plain text, used where cards are unavailable.
func (c *PushCard) PlainText() string {
	lines := []string{c.Tag + "：" + c.Title, "原因：" + c.Reason}
	if c.Required != "" {
		lines = append(lines, "待处理："+c.Required)
	}
	for _, row := range c.Rows {
		lines = append(lines, row[0]+"："+row[1])
	}
	if c.DetailsURL != "" {
		lines = append(lines, "查看详情：", c.DetailsURL)
	}
	return strings.Join(lines, "\n")
}

// feishuCardJSON renders a message card (JSON 1.0). User text is plain_text
// so names and reasons cannot inject Markdown. Buttons carry the action and
// delivery in value; clicks arrive as card.action.trigger callbacks.
func feishuCardJSON(c *PushCard) []byte {
	plain := func(text string) map[string]any { return map[string]any{"tag": "plain_text", "content": text} }
	elements := []any{map[string]any{"tag": "div", "text": plain("原因：" + c.Reason)}}
	if c.Required != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": plain("待处理：" + c.Required)})
	}
	fields := []any{}
	for _, row := range c.Rows {
		fields = append(fields, map[string]any{"is_short": true, "text": plain(row[0] + "：" + row[1])})
	}
	elements = append(elements, map[string]any{"tag": "div", "fields": fields})
	buttons := []any{}
	for _, a := range c.Actions {
		kind := "primary"
		if a.Danger {
			kind = "danger"
		}
		buttons = append(buttons, map[string]any{"tag": "button", "text": plain(a.Text), "type": kind, "value": map[string]string{"af_action": a.Key, "af_delivery": c.DeliveryID}})
	}
	if c.DetailsURL != "" {
		buttons = append(buttons, map[string]any{"tag": "button", "text": plain("查看详情"), "type": "default", "url": c.DetailsURL})
	}
	elements = append(elements, map[string]any{"tag": "action", "actions": buttons})
	template := "orange"
	if c.Tag == "执行失败" {
		template = "red"
	}
	card := map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"template": template, "title": plain(c.Tag + "：" + c.Title)},
		"elements": elements,
	}
	out, _ := json.Marshal(card)
	return out
}

// dingtalkCardParams fills the variables of the administrator's card
// template (see docs/im-channels.md): title, content (Markdown), detail_url,
// status and one show_<action> switch per button.
func dingtalkCardParams(c *PushCard) map[string]string {
	lines := []string{"**原因**：" + dingtalkMarkdownText(c.Reason)}
	if c.Required != "" {
		lines = append(lines, "**待处理**："+dingtalkMarkdownText(c.Required))
	}
	for _, row := range c.Rows {
		lines = append(lines, "**"+row[0]+"**："+dingtalkMarkdownText(row[1]))
	}
	params := map[string]string{"title": c.Tag + "：" + c.Title, "content": strings.Join(lines, "\n\n"), "detail_url": c.DetailsURL, "status": ""}
	for _, key := range PushCardKeys {
		params["show_"+key] = "false"
	}
	for _, a := range c.Actions {
		params["show_"+a.Key] = "true"
		params["text_"+a.Key] = a.Text
	}
	return params
}

// dingtalkMarkdownText neutralizes Markdown control characters in user text.
func dingtalkMarkdownText(text string) string {
	return strings.NewReplacer("*", "＊", "_", "＿", "[", "［", "]", "］", "#", "＃", "`", "'", "<", "＜", ">", "＞").Replace(text)
}

// dingtalkCardTrack is the card instance ID: the delivery plus its send key,
// so a resend (new generation) creates a new card.
func dingtalkCardTrack(msg OutboundMessage) string {
	key := msg.SendKey
	if len(key) > 12 {
		key = key[:12]
	}
	return msg.Card.DeliveryID + "." + key
}

// dingtalkCardDelivery extracts the delivery ID from a card instance ID.
func dingtalkCardDelivery(track string) string {
	if i := strings.LastIndex(track, "."); i > 0 {
		return track[:i]
	}
	return track
}

// deliverCard creates the card instance and delivers it to the robot private
// chat of the staff ID, with Stream callbacks.
func (d *dingtalkChannel) deliverCard(ctx context.Context, bot domain.NotificationBot, token string, msg OutboundMessage) SendResult {
	body := map[string]any{
		"cardTemplateId":          strings.TrimSpace(bot.CardTemplateID),
		"outTrackId":              dingtalkCardTrack(msg),
		"callbackType":            "STREAM",
		"cardData":                map[string]any{"cardParamMap": dingtalkCardParams(msg.Card)},
		"openSpaceId":             "dtv1.card//IM_ROBOT." + msg.ChatID,
		"imRobotOpenSpaceModel":   map[string]any{"supportForward": false, "lastMessageI18n": map[string]string{"ZH_CN": cardText(msg.Card.Tag+"："+msg.Card.Title, 60)}},
		"imRobotOpenDeliverModel": map[string]any{"spaceType": "IM_ROBOT", "robotCode": bot.BotID},
		"userIdType":              1,
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.api+"/v1.0/card/instances/createAndDeliver", bytes.NewReader(raw))
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
	return classifyDingTalkCard(resp.StatusCode, data)
}

// classifyDingTalkCard maps the createAndDeliver answer: the instance and
// each delivery report success separately.
func classifyDingTalkCard(status int, data []byte) SendResult {
	var out struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Result  struct {
			DeliverResults []struct {
				Success   bool   `json:"success"`
				CarrierID string `json:"carrierId"`
				ErrorMsg  string `json:"errorMsg"`
			} `json:"deliverResults"`
		} `json:"result"`
	}
	_ = json.Unmarshal(data, &out)
	switch {
	case status == http.StatusTooManyRequests:
		return SendResult{Outcome: NotDelivered, Code: "429", Retryable: true, SafeDetail: "钉钉限流，稍后重试"}
	case status >= 500:
		return SendResult{Outcome: Unknown, Code: strconv.Itoa(status), SafeDetail: "钉钉返回服务端错误，卡片送达结果未知"}
	case status == http.StatusUnauthorized || (status >= 400 && strings.Contains(strings.ToLower(out.Code), "accesstoken")):
		return SendResult{Outcome: NotDelivered, Code: "token_invalid", Retryable: true, SafeDetail: "钉钉访问凭证已失效，稍后重试"}
	case status != http.StatusOK:
		code := out.Code
		if code == "" {
			code = strconv.Itoa(status)
		}
		detail := "钉钉拒绝了这张卡片（" + truncate(code, 64) + "）"
		if strings.Contains(code, "template") {
			detail += "：请检查互动卡片模板 ID 是否正确、模板是否已发布"
		}
		return SendResult{Outcome: Rejected, Code: truncate(code, 64), SafeDetail: detail}
	}
	if len(out.Result.DeliverResults) == 0 {
		return SendResult{Outcome: Unknown, Code: "unparsed", SafeDetail: "钉钉卡片响应无法识别，送达结果未知"}
	}
	first := out.Result.DeliverResults[0]
	if !first.Success {
		return SendResult{Outcome: Rejected, Code: "deliver_failed", SafeDetail: "钉钉未能投放卡片：" + truncate(first.ErrorMsg, 120)}
	}
	return SendResult{Outcome: Delivered, PlatformMessageID: truncate(first.CarrierID, 191)}
}

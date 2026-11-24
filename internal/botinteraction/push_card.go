package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
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
	// DraftID marks a write-confirmation card: buttons carry the draft
	// instead of a delivery. Body is free text shown above the rows.
	DraftID      string
	Body         string
	DetailsLabel string
	Title        string
	Tag          string // 待人工确认 or 执行失败
	Reason       string
	Required     string
	Rows         [][2]string
	Actions      []PushCardAction
	DetailsURL   string
	// Selects are optional drop-downs (the repository and the SOP of a
	// new requirement).
	Selects []*PushCardSelect
	// Status, when set, marks the card as handled: it is shown instead of
	// the buttons and the selection.
	Status string
	// ClarifyID marks a chat clarification card: buttons carry the
	// session and their own reference (PushCardAction.Ref).
	ClarifyID string
	// Hint is shown below the rows (how to answer a node question).
	Hint string
	// RequiredLabel replaces "待处理" before Required.
	RequiredLabel string
	// lockedSelects are the drop-downs of a handled card (Done).
	lockedSelects []*PushCardSelect
}

// PushCardSelect is a drop-down whose change comes back as Key.
type PushCardSelect struct {
	Key         string
	Label       string
	Placeholder string
	Initial     string
	Options     []PushCardOption
}

// PushCardOption is one drop-down entry.
type PushCardOption struct {
	Value string
	Text  string
}

// PushCardAction is one button.
type PushCardAction struct {
	Key    string
	Text   string
	Danger bool
	// Ref is passed back with the click (clarification cards).
	Ref string
}

// PushCardKeys are the action keys a card button may carry.
// "answer" submits the card's answer input (Feishu node questions).
var PushCardKeys = []string{"approve", "retry", "reject", "pass", "resume", "decline", "answer"}

// PushCardAnswerField is the name of the answer input of a node question
// card.
const PushCardAnswerField = "answer"

// Draft confirmation card actions.
const (
	DraftActionConfirm      = "draft_confirm"
	DraftActionConfirmStart = "draft_confirm_start"
	DraftActionCancel       = "draft_cancel"
	// DraftActionSOP picks the SOP of the draft's new requirement.
	DraftActionSOP = "draft_sop"
	// DraftActionRepo picks the repository of the draft's new requirement.
	DraftActionRepo = "draft_repo"
)

// DraftAction reports whether key is a draft confirmation action.
func DraftAction(key string) bool {
	return key == DraftActionConfirm || key == DraftActionConfirmStart || key == DraftActionCancel || key == DraftActionSOP || key == DraftActionRepo
}

// DraftSelectAction reports whether key is a drop-down of a draft card.
func DraftSelectAction(key string) bool {
	return key == DraftActionSOP || key == DraftActionRepo
}

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
	if question := run.Nodes[item.NodeID].Question(); question != "" && item.EventType == "waiting" {
		card.Tag, card.Required, card.RequiredLabel = "需要补充信息", cardText(question, 300), "问题"
		if bot, err := s.Store.NotificationBot(item.BotID); err == nil {
			card.Hint = domain.NodeAnswerHint(bot.ChannelKind(), item.TargetType)
			if bot.ChannelKind() == domain.BotChannelFeishu {
				card.Hint = "在下方填写答案并提交，答案会提交给这个节点，节点按流程继续。"
			}
		}
	}
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
	if DraftAction(key) || ClarifyAction(key) {
		return true
	}
	for _, k := range PushCardKeys {
		if k == key {
			return true
		}
	}
	return false
}

func (c *PushCard) requiredLabel() string {
	if c.RequiredLabel != "" {
		return c.RequiredLabel
	}
	return "待处理"
}

// PlainText is the card as plain text, used where cards are unavailable.
func (c *PushCard) PlainText() string {
	lines := []string{c.Tag + "：" + c.Title, "原因：" + c.Reason}
	if c.Required != "" {
		lines = append(lines, c.requiredLabel()+"："+c.Required)
	}
	for _, row := range c.Rows {
		lines = append(lines, row[0]+"："+row[1])
	}
	if c.Hint != "" {
		lines = append(lines, c.Hint)
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
	elements := []any{}
	if c.Body != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": plain(c.Body)})
	}
	if c.Reason != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": plain("原因：" + c.Reason)})
	}
	if c.Required != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": plain(c.requiredLabel() + "：" + c.Required)})
	}
	fields := []any{}
	for _, row := range c.Rows {
		fields = append(fields, map[string]any{"is_short": true, "text": plain(row[0] + "：" + row[1])})
	}
	if len(fields) > 0 {
		elements = append(elements, map[string]any{"tag": "div", "fields": fields})
	}
	if c.Hint != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": plain(c.Hint)})
	}
	value := func(key, ref string) map[string]string {
		switch {
		case c.ClarifyID != "":
			return map[string]string{"af_action": key, "af_clarify": c.ClarifyID, "af_ref": ref}
		case c.DraftID != "":
			return map[string]string{"af_action": key, "af_draft": c.DraftID}
		}
		return map[string]string{"af_action": key, "af_delivery": c.DeliveryID}
	}
	for _, choice := range c.Selects {
		options := []any{}
		for _, o := range choice.Options {
			options = append(options, map[string]any{"text": plain(o.Text), "value": o.Value})
		}
		sel := map[string]any{"tag": "select_static", "placeholder": plain(choice.Placeholder), "options": options, "value": value(choice.Key, "")}
		if choice.Initial != "" {
			sel["initial_option"] = choice.Initial
		}
		elements = append(elements, map[string]any{"tag": "div", "text": plain(choice.Label)}, map[string]any{"tag": "action", "actions": []any{sel}})
	}
	if c.Status != "" {
		elements = append(elements, map[string]any{"tag": "note", "elements": []any{plain(c.Status)}})
	}
	buttons := []any{}
	for _, a := range c.Actions {
		if a.Key == "answer" {
			// Card JSON 1.0 form: the input comes back as form_value.
			elements = append(elements, map[string]any{"tag": "form", "name": "af_answer_form", "elements": []any{
				map[string]any{"tag": "input", "name": PushCardAnswerField, "placeholder": plain("填写答案"), "max_length": 1000, "width": "fill"},
				map[string]any{"tag": "button", "text": plain(a.Text), "type": "primary", "action_type": "form_submit", "name": "af_answer_submit", "value": value(a.Key, a.Ref)},
			}})
			continue
		}
		kind := "primary"
		if a.Danger {
			kind = "danger"
		}
		buttons = append(buttons, map[string]any{"tag": "button", "text": plain(a.Text), "type": kind, "value": value(a.Key, a.Ref)})
	}
	if c.DetailsURL != "" {
		label := c.DetailsLabel
		if label == "" {
			label = "查看详情"
		}
		buttons = append(buttons, map[string]any{"tag": "button", "text": plain(label), "type": "default", "url": c.DetailsURL})
	}
	if len(buttons) > 0 {
		elements = append(elements, map[string]any{"tag": "action", "actions": buttons})
	}
	template := "orange"
	switch {
	case c.Status != "":
		template = "grey"
	case c.Tag == "执行失败":
		template = "red"
	case c.DraftID != "" || c.ClarifyID != "":
		template = "blue"
	}
	card := map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"template": template, "title": plain(c.headerTitle())},
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
		lines = append(lines, "**"+c.requiredLabel()+"**："+dingtalkMarkdownText(c.Required))
	}
	for _, row := range c.Rows {
		lines = append(lines, "**"+row[0]+"**："+dingtalkMarkdownText(row[1]))
	}
	if c.Hint != "" {
		lines = append(lines, dingtalkMarkdownText(c.Hint))
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
// chat of the staff ID, or into a group (ChatType "group"), with Stream
// callbacks.
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
	if msg.ChatType == "group" {
		// A group card is delivered into the group by the robot.
		delete(body, "imRobotOpenSpaceModel")
		delete(body, "imRobotOpenDeliverModel")
		body["openSpaceId"] = "dtv1.card//IM_GROUP." + msg.ChatID
		body["imGroupOpenSpaceModel"] = map[string]any{"supportForward": false, "lastMessageI18n": map[string]string{"ZH_CN": cardText(msg.Card.Tag+"："+msg.Card.Title, 60)}}
		body["imGroupOpenDeliverModel"] = map[string]any{"robotCode": bot.BotID}
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

// draftConfirmMarker starts the confirmation section the platform appends to
// a chat reply with write drafts (see httpapi.confirmBotDrafts).
const draftConfirmMarker = "\n\n以下操作需要你在平台确认后才会执行"

// DraftCardFor builds the write-confirmation card of a chat reply, or nil
// when the reply produced no pending draft.
func (s *Service) DraftCardFor(item domain.BotInteraction, publicURL string) *PushCard {
	draft, ok := s.Store.BotPendingDraftForInteraction(item.ID)
	if !ok {
		return nil
	}
	return draftCard(draft, item.Reply, publicURL)
}

// draftCardByID rebuilds the card of a draft (after a click or a choice).
func (s *Service) draftCardByID(draft domain.BotPendingDraft) *PushCard {
	reply := ""
	if item, err := s.Store.BotInteraction(draft.InteractionID); err == nil {
		reply = item.Reply
	}
	return draftCard(draft, reply, s.publicURL())
}

// draftChoiceSuffix is the repository and SOP note of a new requirement in
// the summary; on the card they are chosen in the drop-downs instead.
var draftChoiceSuffix = regexp.MustCompile(`(（仓库：[^）]*）)?（SOP：[^）]*）$`)

// draftCard is the model's explanation, the frozen operations, an SOP
// selection when the draft creates one requirement, confirm /
// confirm-and-start / cancel buttons and a link to the confirmation page.
func draftCard(draft domain.BotPendingDraft, reply, publicURL string) *PushCard {
	body := reply
	if i := strings.Index(body, draftConfirmMarker); i >= 0 {
		body = body[:i]
	}
	ops := strings.Split(strings.TrimSpace(draft.Summary), "\n")
	card := &PushCard{DraftID: draft.ID, Tag: "待你确认", Title: fmt.Sprintf("%d 项操作", len(ops)), Body: strings.TrimSpace(body)}
	for i, op := range ops {
		if op = strings.TrimSpace(op); op == "" {
			continue
		}
		if draft.SOP != nil && i == draft.SOP.Index {
			// The SOP is chosen in the selection below.
			op = draftChoiceSuffix.ReplaceAllString(op, "")
		}
		card.Rows = append(card.Rows, [2]string{"操作", op})
	}
	if draft.SOP != nil {
		if len(draft.SOP.Repositories) > 0 {
			def := "不绑定仓库（项目未设默认仓库）"
			if draft.SOP.DefaultRepositoryName != "" {
				def = "项目默认仓库（" + draft.SOP.DefaultRepositoryName + "）"
			}
			repo := &PushCardSelect{Key: DraftActionRepo, Label: "仓库", Initial: draft.SelectedRepository(), Placeholder: "选择仓库"}
			repo.Options = append(repo.Options, PushCardOption{Value: domain.BotDraftDefaultSOP, Text: def})
			for _, r := range draft.SOP.Repositories {
				repo.Options = append(repo.Options, PushCardOption{Value: r.ID, Text: r.Name})
			}
			card.Selects = append(card.Selects, repo)
		}
		sel := &PushCardSelect{Key: DraftActionSOP, Label: "SOP", Initial: draft.Selected(), Placeholder: "选择 SOP"}
		sel.Options = append(sel.Options, PushCardOption{Value: domain.BotDraftDefaultSOP, Text: "项目默认 SOP（" + draft.SOP.DefaultName + "）"})
		for _, wf := range draft.SOP.Workflows {
			sel.Options = append(sel.Options, PushCardOption{Value: wf.ID, Text: wf.Name})
		}
		card.Selects = append(card.Selects, sel)
	}
	card.Required = fmt.Sprintf("%d 分钟内有效，只有发起人可以确认；每份操作只执行一次", int(repo.BotDraftTTL.Minutes()))
	card.Actions = []PushCardAction{{Key: DraftActionConfirm, Text: "确认执行"}}
	if strings.Contains(draft.Summary, "（SOP：") {
		card.Actions = append(card.Actions, PushCardAction{Key: DraftActionConfirmStart, Text: "确认并启动 SOP"})
	}
	card.Actions = append(card.Actions, PushCardAction{Key: DraftActionCancel, Text: "取消", Danger: true})
	if base := strings.TrimRight(publicURL, "/"); base != "" {
		card.DetailsURL, card.DetailsLabel = base+"/bot-approve.html#draft="+draft.ID, "在平台确认"
	}
	if draft.Status != repo.BotDraftPending {
		card.Done(draftStatusLine(draft))
	}
	return card
}

// draftStatusLine is the card status of a draft that is no longer pending.
func draftStatusLine(d domain.BotPendingDraft) string {
	switch d.Status {
	case repo.BotDraftExecuting:
		return "正在执行，结果会回复在会话里"
	case repo.BotDraftExecuted:
		return "已执行，结果见会话里的回复"
	case repo.BotDraftFailed:
		return "已处理，部分或全部未完成，详情见会话里的回复"
	case repo.BotDraftCancelled:
		return "已取消，没有执行"
	case repo.BotDraftExpired:
		return "已过期，没有执行"
	}
	return "这份操作已处理"
}

// Done turns the card into its handled state: the status line replaces the
// buttons and the selection, so a second click is not possible.
func (c *PushCard) Done(status string) {
	if len(c.Selects) > 0 {
		// WeCom updates a card with drop-downs as the same card type,
		// with the drop-downs locked.
		c.lockedSelects = c.Selects
	}
	c.Status, c.Actions, c.Selects, c.Hint = status, nil, nil, ""
}

func (s *Service) publicURL() string {
	if s.PublicURL == nil {
		return ""
	}
	raw := strings.TrimSpace(s.PublicURL())
	if u, err := url.Parse(raw); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return raw
}

func (c *PushCard) headerTitle() string {
	tag := c.Tag
	if c.Status != "" {
		tag = "已处理"
	}
	return tag + "：" + c.Title
}

// feishuCardObject is the card as a JSON object (callback responses).
func feishuCardObject(c *PushCard) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(feishuCardJSON(c), &out)
	return out
}

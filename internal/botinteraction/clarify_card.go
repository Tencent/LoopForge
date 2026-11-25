package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"strings"
)

// Chat clarification (Feishu, DingTalk). The platform posts each round's
// numbered questions and the final conclusion into the conversation as
// clarify_post interactions (sent as new messages, SendRoute push_chat). On
// Feishu they are cards with buttons; DingTalk gets the same text and the
// commands it names. Clicks come back as clarify_* actions.

// Clarification actions.
const (
	ActionClarifyPost = "clarify_post"
	// ActionChatPost is a prepared platform message sent into a WeCom
	// conversation over the live connection (progress of a run started
	// there).
	ActionChatPost = "chat_post"

	ClarifyActionNext      = "clarify_next"
	ClarifyActionEnd       = "clarify_end"
	ClarifyActionSave      = "clarify_save"
	ClarifyActionSaveStart = "clarify_save_start"
	ClarifyActionYes       = "clarify_yes"
	ClarifyActionNo        = "clarify_no"
)

// Kinds of clarify_post messages (BotInteraction.Quote).
const (
	ClarifyPostQuestions  = "questions"
	ClarifyPostConclusion = "conclusion"
	ClarifyPostNotice     = "notice"
)

// ClarifyAction reports whether key is a clarification card action.
func ClarifyAction(key string) bool {
	switch key {
	case ClarifyActionNext, ClarifyActionEnd, ClarifyActionSave, ClarifyActionSaveStart, ClarifyActionYes, ClarifyActionNo:
		return true
	}
	return false
}

// ClarifyActionLabel names a clarification action for status lines.
func ClarifyActionLabel(key string) string {
	return map[string]string{ClarifyActionNext: "提交回答，进入下一轮", ClarifyActionEnd: "结束澄清", ClarifyActionSave: "保存澄清结果", ClarifyActionSaveStart: "保存并启动 SOP", ClarifyActionYes: "是", ClarifyActionNo: "不是"}[key]
}

// clarifyPostCard turns a clarify_post message into its card: the first
// line is the title, the rest the body. Notices stay plain text.
func clarifyPostCard(item domain.BotInteraction, text string) *PushCard {
	head, body, _ := strings.Cut(strings.TrimSpace(text), "\n")
	card := &PushCard{ClarifyID: item.DeliveryID, Title: strings.TrimSpace(head), Body: strings.TrimSpace(body)}
	switch item.Quote {
	case ClarifyPostQuestions:
		card.Tag = "澄清问题"
		card.Actions = []PushCardAction{{Key: ClarifyActionNext, Text: "提交回答，进入下一轮", Ref: item.ID}, {Key: ClarifyActionEnd, Text: "结束澄清", Danger: true, Ref: item.ID}}
	case ClarifyPostConclusion:
		card.Tag = "澄清结论"
		card.Required = "需求负责人、发起人或项目维护者可以保存；同一份结论只保存一次"
		card.Actions = []PushCardAction{{Key: ClarifyActionSave, Text: "保存澄清结果", Ref: item.ID}, {Key: ClarifyActionSaveStart, Text: "保存并启动 SOP", Ref: item.ID}, {Key: ClarifyActionEnd, Text: "结束澄清", Danger: true, Ref: item.ID}}
	default:
		return nil
	}
	return card
}

// clarifyConfirmCard asks the speaker whether a message answers question N.
func clarifyConfirmCard(sessionID string, p domain.BotClarifyPending) *PushCard {
	label := domain.QuestionLabels([]int{p.N})
	return &PushCard{ClarifyID: sessionID, Tag: "请确认", Title: "这是对 " + label + " 的回答吗？", Body: "「" + cardText(p.Text, 200) + "」", Required: "只有说这句话的人可以确认",
		Actions: []PushCardAction{{Key: ClarifyActionYes, Text: "是，记为 " + label, Ref: p.ID}, {Key: ClarifyActionNo, Text: "不是", Danger: true, Ref: p.ID}}}
}

// clarifyCardFor is the card of an outgoing message, if any: a
// clarification question or conclusion, or the confirmation attached to a
// reply the Bot is unsure about.
func (s *Service) clarifyCardFor(item domain.BotInteraction) *PushCard {
	if item.Action == ActionClarifyPost {
		return clarifyPostCard(item, item.Reply)
	}
	if item.Action != "" {
		return nil
	}
	sess, ok := s.Store.ActiveBotClarifySession(item.ConversationID)
	if !ok {
		return nil
	}
	for _, p := range sess.Pending {
		if p.ID == item.ID {
			return clarifyConfirmCard(sess.ID, p)
		}
	}
	return nil
}

// chatMember checks that a chat sender is bound to a platform account that
// may act in the Bot's project (not a viewer). It returns the account and
// a refusal for the clicker otherwise.
func (s *Service) chatMember(bot domain.NotificationBot, sender string) (domain.User, string) {
	binding, err := s.Store.BotUserBinding(bot.IdentityScope(), sender)
	if err != nil || (bot.ChannelKind() != domain.BotChannelWeCom && binding.ConfigID != bot.ID) {
		return domain.User{}, "请先私聊机器人绑定平台账号"
	}
	user, ok := s.Store.User(binding.UserID)
	if !ok {
		return user, "绑定的平台账号不可用"
	}
	project, ok := s.Store.Project(bot.ProjectID)
	if !ok {
		return user, "机器人所属项目不存在"
	}
	if project.OwnerID == user.ID || s.Store.IsPlatformAdmin(user.LoginName) {
		return user, ""
	}
	for _, m := range project.Members {
		if m.UserID == user.ID && !strings.EqualFold(strings.TrimSpace(m.Role), "viewer") {
			return user, ""
		}
	}
	return user, "你的平台账号没有该项目的执行权限"
}

// DisplayName is the name shown for a platform user in chats.
func DisplayName(u domain.User) string {
	if strings.TrimSpace(u.ChineseName) != "" {
		return strings.TrimSpace(u.ChineseName)
	}
	return u.LoginName
}

// acceptClarifyAction handles the buttons of clarification cards. Anyone
// bound to the project may move to the next round; saving and ending are
// limited to the requirement's responsible person, the starter and project
// maintainers; a "这是对 Qn 的回答吗" card only to the speaker. The click is
// queued in the session's conversation, threaded under the card, and the
// card is locked with who clicked.
func (s *Service) acceptClarifyAction(bot domain.NotificationBot, item domain.BotInteraction, ev InboundEvent) (InboundAck, error) {
	sess, err := s.Store.BotClarifySession(ev.DeliveryID)
	if err != nil || sess.ConfigID != bot.ID {
		return InboundAck{Ignored: "clarify_unknown", Toast: "这次澄清已失效"}, nil
	}
	user, refusal := s.chatMember(bot, ev.SenderID)
	if refusal != "" {
		return InboundAck{Ignored: "not_member", Toast: refusal}, nil
	}
	var card *PushCard
	switch ev.Action {
	case ClarifyActionYes, ClarifyActionNo:
		var pending *domain.BotClarifyPending
		for i := range sess.Pending {
			if sess.Pending[i].ID == ev.Option {
				pending = &sess.Pending[i]
			}
		}
		if pending == nil {
			return InboundAck{Ignored: "clarify_settled", Toast: "这条消息已经确认过了"}, nil
		}
		if pending.Sender != ev.SenderID {
			return InboundAck{Ignored: "not_speaker", Toast: "只有说这句话的人可以确认"}, nil
		}
		card = clarifyConfirmCard(sess.ID, *pending)
	default:
		post, err := s.Store.BotInteraction(ev.Option)
		if err != nil || post.Action != ActionClarifyPost || post.DeliveryID != sess.ID {
			return InboundAck{Ignored: "clarify_unknown", Toast: "这张卡片已失效"}, nil
		}
		card = clarifyPostCard(post, post.Reply)
		if card == nil {
			return InboundAck{Ignored: "clarify_unknown", Toast: "这张卡片已失效"}, nil
		}
		if !domain.BotClarifyActive(sess.Status) {
			card.Done("这次澄清已结束")
			return InboundAck{Ignored: "clarify_closed", Toast: "这次澄清已结束", Card: card}, nil
		}
		switch ev.Action {
		case ClarifyActionNext:
			if sess.Status != domain.BotClarifyAsking && sess.Status != domain.BotClarifyPaused {
				return InboundAck{Ignored: "clarify_state", Toast: "当前不在收集回答的阶段"}, nil
			}
		case ClarifyActionSave, ClarifyActionSaveStart:
			if sess.Status != domain.BotClarifyConcluded {
				return InboundAck{Ignored: "clarify_state", Toast: "这份结论已在保存或已处理"}, nil
			}
			fallthrough
		case ClarifyActionEnd:
			if !s.clarifySaver(bot, sess, user) {
				return InboundAck{Ignored: "not_saver", Toast: "只有需求负责人、发起澄清的人或项目维护者可以操作"}, nil
			}
		}
	}
	kind, chat := sess.ChatType, sess.ChatID
	item.Content, item.ContentLength = "", 0
	item.DeliveryID, item.Quote = sess.ID, ev.Option
	item.ReplyRoute = ev.Route.MessageID
	if item.ReplyRoute == "" {
		item.SendRoute = repo.BotSendRoutePushChat
	}
	inserted, err := s.Store.EnqueueBotInteraction(item, kind, chat)
	if err != nil {
		return InboundAck{}, err
	}
	card.Done(DisplayName(user) + " 已选择「" + ClarifyActionLabel(ev.Action) + "」")
	return InboundAck{Accepted: inserted, InteractionID: item.ID, Toast: "已收到", Card: card}, nil
}

// clarifySaver applies domain.ClarifySaveAllowed with current data.
func (s *Service) clarifySaver(bot domain.NotificationBot, sess domain.BotClarifySession, user domain.User) bool {
	project, ok := s.Store.Project(bot.ProjectID)
	if !ok {
		return false
	}
	issue, _ := s.Store.Issue(sess.IssueID)
	return domain.ClarifySaveAllowed(project, issue, sess, user.ID)
}

// SendChatPost sends a notification as a prepared message into its
// conversation over the Bot's live connection (WeCom) and waits for the send
// result. A retry of the delivery re-sends a failed message.
func (s *Service) SendChatPost(ctx context.Context, bot domain.NotificationBot, delivery domain.NotificationDelivery, text string) error {
	text = domain.NotificationText(text, 16000)
	item := domain.BotInteraction{ID: notificationInteractionID(bot.ID, ActionChatPost, delivery), BotID: bot.ID, ProjectID: bot.ProjectID, SenderID: delivery.TargetID, Action: ActionChatPost, DeliveryID: delivery.ID, Content: text, ContentLength: len([]rune(text)), MsgType: "text", SendRoute: repo.BotSendRoutePushChat}
	if _, err := s.Store.EnqueueBotInteraction(item, delivery.TargetType, delivery.TargetID); err != nil {
		return err
	}
	if err := s.Store.RequeueBotCard(item.ID); err != nil {
		return err
	}
	_, err := s.waitSent(ctx, item.ID, "消息")
	return err
}

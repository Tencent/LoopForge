package botinteraction

import (
	"agentflow/internal/credcrypt"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	unsupportedMessageText = "[不支持的消息类型，请发送文字]"
	// New channels accept private text up to this many characters.
	channelTextLimit = 8000

	noticeTenantPending = "机器人正在完成企业确认，请稍后重发消息。"
	noticeUnsupported   = "目前只支持文字消息，请发送文字。"
	noticeTooLong       = "消息超过 8000 字，请精简后重新发送。"
)

// interactionFromEvent validates a channel-neutral event against the Bot
// configuration of the receiving connection and builds the interaction to
// persist. A non-empty reason means the event must be dropped.
func interactionFromEvent(bot domain.NotificationBot, ev InboundEvent) (domain.BotInteraction, string) {
	switch {
	case ev.AppID != bot.BotID:
		return domain.BotInteraction{}, "app_mismatch"
	case ev.MessageID == "" || ev.SenderID == "":
		return domain.BotInteraction{}, "missing_identity"
	case ev.ChatID == "" || (ev.ChatType != "single" && ev.ChatType != "group"):
		return domain.BotInteraction{}, "invalid_chat"
	case len(ev.ChatID) > 191 || len(ev.SenderID) > 191:
		return domain.BotInteraction{}, "identity_too_long"
	case strings.TrimSpace(ev.Text) == "" && ev.Action == "" && (ev.Channel == "" || ev.Channel == ChannelWeCom):
		// New channels classify empty and non-text messages in accept.
		return domain.BotInteraction{}, "empty"
	}
	return domain.BotInteraction{
		ID:              repo.BotMessageID(bot.IdentityScope(), ev.MessageID),
		BotID:           bot.ID,
		ProjectID:       bot.ProjectID,
		SenderID:        ev.SenderID,
		RequestID:       ev.Route.RequestID,
		Content:         ev.Text,
		Quote:           ev.Quote,
		Action:          ev.Action,
		DeliveryID:      ev.DeliveryID,
		CardTaskID:      ev.CardTaskID,
		PlatformEventID: truncate(ev.EventID, 191),
		MsgType:         truncate(ev.MsgType, 32),
		ContentLength:   utf8.RuneCountInString(ev.Text),
		ReplyRoute:      ev.Route.MessageID,
		ReplyTo:         truncate(ev.ParentID, 191),
	}, ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// accept is the common inbound sink: validate against the current
// configuration, classify and enqueue in one bounded transaction. Duplicates
// return Accepted=false without error so the adapter acknowledges them;
// storage errors are returned for redelivery.
func (s *Service) accept(ctx context.Context, connected domain.NotificationBot, ev InboundEvent) (InboundAck, error) {
	// Tenant locking, names and enable flags change without reconnecting.
	bot, err := s.Store.NotificationBot(connected.ID)
	if err != nil {
		return InboundAck{}, err
	}
	if !bot.Enabled || !bot.InteractionEnabled || bot.BotID != connected.BotID || bot.ChannelKind() != connected.ChannelKind() {
		return s.ignore(ev, "bot_inactive"), nil
	}
	item, reason := interactionFromEvent(bot, ev)
	if reason != "" {
		return s.ignore(ev, reason), nil
	}
	if ev.Route.Webhook != "" {
		// An authorization-bearing session webhook is stored sealed and
		// bound to this interaction; its expiry decides route_expired.
		if item.ReplyRoute, err = s.Store.SealBotRoute(bot.ID, item.ID, ev.Route.Webhook); err != nil {
			return InboundAck{}, err
		}
		if !ev.Route.ExpiresAt.IsZero() {
			item.RouteExpiresAtUS = ev.Route.ExpiresAt.UnixMicro()
		}
	}
	if bot.ChannelKind() != domain.BotChannelWeCom {
		unmentioned := ev.ChatType == "group" && !ev.Mentioned && ev.Action == ""
		s.Store.NoteBotConnectionEvent(bot.LeaseKey(), unmentioned)
		if unmentioned && !s.repliesToNodeQuestion(bot, ev) {
			// Group chats: only messages that @ the Bot, or reply to a
			// node question it asked in this group.
			return s.ignore(ev, "group_not_mentioned"), nil
		}
		notice, gated, err := s.tenantGate(bot, ev)
		if err != nil {
			return InboundAck{}, err
		}
		if gated {
			if notice == "" {
				return s.ignore(ev, "tenant_mismatch"), nil
			}
			return s.enqueueNotice(item, ev, notice)
		}
		if ev.Action != "" {
			return s.acceptCardAction(bot, item, ev)
		}
		switch {
		case ev.MsgType != "text":
			return s.enqueueNotice(item, ev, noticeUnsupported)
		case item.ContentLength > channelTextLimit:
			return s.enqueueNotice(item, ev, noticeTooLong)
		case strings.TrimSpace(ev.Text) == "":
			return s.ignore(ev, "empty"), nil
		}
	}
	if bot.ChannelKind() == domain.BotChannelWeCom && (DraftAction(ev.Action) || ClarifyAction(ev.Action)) {
		// WeCom confirmation and clarification cards follow the same
		// rules as on Feishu.
		return s.acceptCardAction(bot, item, ev)
	}
	inserted, err := s.Store.EnqueueBotInteraction(item, ev.ChatType, ev.ChatID)
	if err != nil {
		log.Printf("%s event enqueue failed action=%s", channelLabel(ev.Channel), item.Action)
		return InboundAck{}, err
	}
	return InboundAck{Accepted: inserted, InteractionID: item.ID}, nil
}

// repliesToNodeQuestion reports whether a group message replies to a
// question of a waiting node this Bot sent to the same group and the node
// still asks.
func (s *Service) repliesToNodeQuestion(bot domain.NotificationBot, ev InboundEvent) bool {
	if ev.ParentID == "" {
		return false
	}
	d, ok := s.Store.NotificationDeliveryByPlatformMessage(bot.ID, ev.ParentID)
	if !ok {
		post, found := s.Store.BotInteractionByPlatformMessage(bot.ID, ev.ParentID)
		if !found || post.Action != "notification" || post.DeliveryID == "" {
			return false
		}
		var err error
		if d, err = s.Store.NotificationDelivery(post.DeliveryID); err != nil {
			return false
		}
	}
	return d.ProjectID == bot.ProjectID && d.TargetType == "group" && d.TargetID == ev.ChatID && s.NodeQuestion(d) != ""
}

// acceptCardAction handles a Feishu or DingTalk card click.
//
// Notification cards (approve, retry, …): only the recipient of the
// notification is placed in its conversation; anyone else lands in a
// conversation of their own and the action is refused as expired
// (NotificationCardTask checks the conversation against the delivery). The
// result goes out as a private message to the clicker.
//
// Write-confirmation cards (draft_*): only the requester may use them. The
// click is queued in the conversation the request came from, so a group
// request is answered in the group and a private one privately, threaded
// under the card. A SOP pick is stored right away.
//
// The answer carries the updated card: after a click the buttons are
// replaced by a status line, so the user sees it was taken and cannot
// click again.
func (s *Service) acceptCardAction(bot domain.NotificationBot, item domain.BotInteraction, ev InboundEvent) (InboundAck, error) {
	if !pushCardKey(ev.Action) || ev.DeliveryID == "" {
		return s.ignore(ev, "unknown_action"), nil
	}
	if DraftAction(ev.Action) {
		return s.acceptDraftAction(bot, item, ev)
	}
	if ClarifyAction(ev.Action) {
		return s.acceptClarifyAction(bot, item, ev)
	}
	if ev.Action == "answer" && strings.TrimSpace(ev.Text) == "" {
		return InboundAck{Ignored: "empty_answer", Toast: "请先填写答案"}, nil
	}
	delivery, err := s.Store.NotificationDelivery(ev.DeliveryID)
	own := err == nil && delivery.BotID == bot.ID && delivery.ProjectID == bot.ProjectID
	if own && delivery.TargetType == "group" && (ev.CardChatID == "" || ev.CardChatID == delivery.TargetID) {
		return s.acceptGroupCardAction(bot, item, ev, delivery)
	}
	chat := ev.SenderID
	if own && delivery.TargetType == "single" && delivery.TargetID == ev.SenderID {
		chat = delivery.TargetID
	}
	clearCardContent(&item)
	item.SendRoute = repo.BotSendRoutePush
	inserted, err := s.Store.EnqueueBotInteraction(item, "single", chat)
	if err != nil {
		return InboundAck{}, err
	}
	ack := InboundAck{Accepted: inserted, InteractionID: item.ID, Toast: "已收到，处理结果会以消息发给你"}
	if card := s.PushCardFor(delivery, delivery.Content, ""); card != nil {
		card.Done(cardActionStatus("", ev) + "，处理结果会以消息发给你")
		ack.Card = card
	}
	return ack, nil
}

// acceptGroupCardAction handles a notification card sent to a group (a run
// started from that group). Any member bound to a platform account with
// access to the project may click; the action runs as that member. The
// click is queued in the group conversation and answered there (threaded
// under the card on Feishu), and the card shows who handled it.
func (s *Service) acceptGroupCardAction(bot domain.NotificationBot, item domain.BotInteraction, ev InboundEvent, delivery domain.NotificationDelivery) (InboundAck, error) {
	user, refusal := s.chatMember(bot, ev.SenderID)
	if refusal != "" {
		return InboundAck{Ignored: "not_member", Toast: refusal}, nil
	}
	clearCardContent(&item)
	item.ReplyRoute = ev.Route.MessageID
	if item.ReplyRoute == "" {
		item.SendRoute = repo.BotSendRoutePushChat
	}
	inserted, err := s.Store.EnqueueBotInteraction(item, "group", delivery.TargetID)
	if err != nil {
		return InboundAck{}, err
	}
	status := cardActionStatus(DisplayName(user)+" ", ev) + "，结果会发在群里"
	ack := InboundAck{Accepted: inserted, InteractionID: item.ID, Toast: status}
	if card := s.PushCardFor(delivery, delivery.Content, ""); card != nil {
		card.Done(status)
		ack.Card = card
	}
	return ack, nil
}

// clearCardContent drops the text of a card click; an answer keeps it.
func clearCardContent(item *domain.BotInteraction) {
	if item.Action != "answer" {
		item.Content, item.ContentLength = "", 0
	}
	item.Quote = ""
}

// cardActionStatus is the status line of a clicked notification card.
func cardActionStatus(who string, ev InboundEvent) string {
	if ev.Action == "answer" {
		return who + "已提交答案：" + cardText(ev.Text, 100)
	}
	return who + "已提交「" + pushCardActionLabel(ev.Action) + "」"
}

func pushCardActionLabel(key string) string {
	if label := map[string]string{"answer": "提交答案", "approve": "通过", "retry": "重试", "reject": "返工", "pass": "手动通过", "resume": "继续", "decline": "不重试"}[key]; label != "" {
		return label
	}
	return key
}

// acceptDraftAction handles the buttons and the SOP selection of a write
// confirmation card.
func (s *Service) acceptDraftAction(bot domain.NotificationBot, item domain.BotInteraction, ev InboundEvent) (InboundAck, error) {
	now := time.Now().UTC()
	draft, err := s.Store.BotPendingDraft(ev.DeliveryID, now)
	if err != nil || draft.ConfigID != bot.ID {
		return InboundAck{Ignored: "draft_unknown", Toast: "这张卡片已失效，请重新发起"}, nil
	}
	if draft.SenderID != ev.SenderID {
		return InboundAck{Ignored: "not_requester", Toast: "只有发起人可以确认这份操作"}, nil
	}
	if draft.Status != repo.BotDraftPending {
		return InboundAck{Ignored: "draft_settled", Toast: draftStatusLine(draft), Card: s.draftCardByID(draft)}, nil
	}
	if len(ev.Selections) > 0 {
		// A WeCom card submits the repository and the SOP with the
		// operation: store them first, with the same checks as a pick.
		if pick, ok := ev.Selections[DraftActionRepo]; ok && pick != "" && pick != draft.SelectedRepository() {
			if draft, err = s.Store.SetBotPendingDraftRepository(draft.ID, bot.ID, ev.SenderID, pick, now); err != nil {
				return InboundAck{Ignored: "repo_refused", Toast: "所选仓库不可用，请重新发起"}, nil
			}
		}
		if pick, ok := ev.Selections[DraftActionSOP]; ok && pick != "" && pick != draft.Selected() {
			if draft, err = s.Store.SetBotPendingDraftChoice(draft.ID, bot.ID, ev.SenderID, pick, now); err != nil {
				return InboundAck{Ignored: "sop_refused", Toast: "所选 SOP 不可用，请重新发起"}, nil
			}
		}
	}
	if ev.Action == DraftActionSOP {
		picked, err := s.Store.SetBotPendingDraftChoice(draft.ID, bot.ID, ev.SenderID, strings.TrimSpace(ev.Option), now)
		if errors.Is(err, repo.ErrBotDraftNotPending) {
			return InboundAck{Ignored: "sop_refused", Toast: "这个 SOP 不可选", Card: s.draftCardByID(draft)}, nil
		}
		if err != nil {
			return InboundAck{}, err
		}
		return InboundAck{Toast: "已选择 SOP", Card: s.draftCardByID(picked)}, nil
	}
	if ev.Action == DraftActionRepo {
		picked, err := s.Store.SetBotPendingDraftRepository(draft.ID, bot.ID, ev.SenderID, strings.TrimSpace(ev.Option), now)
		if errors.Is(err, repo.ErrBotDraftNotPending) {
			return InboundAck{Ignored: "repo_refused", Toast: "这个仓库不可选", Card: s.draftCardByID(draft)}, nil
		}
		if err != nil {
			return InboundAck{}, err
		}
		return InboundAck{Toast: "已选择仓库", Card: s.draftCardByID(picked)}, nil
	}
	// Answer where the request was made: its conversation, threaded under
	// the card (or the request when the card's message is unknown).
	kind, chat := "single", ev.SenderID
	route := ev.Route.MessageID
	if origin, err := s.Store.BotInteraction(draft.InteractionID); err == nil {
		if conv, err := s.Store.BotConversation(origin.ConversationID); err == nil && conv.BotID == bot.ID {
			kind, chat = conv.ChatType, conv.ChatID
		}
		if route == "" {
			route = origin.ReplyRoute
		}
	}
	item.Content, item.Quote, item.ContentLength = "", "", 0
	if credcrypt.IsSealed(route) {
		// A sealed session webhook belongs to the original interaction.
		route = ""
	}
	item.ReplyRoute = route
	if route == "" && bot.ChannelKind() != domain.BotChannelWeCom {
		item.SendRoute = repo.BotSendRoutePush
		kind, chat = "single", ev.SenderID
	}
	inserted, err := s.Store.EnqueueBotInteraction(item, kind, chat)
	if err != nil {
		return InboundAck{}, err
	}
	status := map[string]string{DraftActionConfirm: "已提交「确认执行」，正在执行，结果会回复在会话里", DraftActionConfirmStart: "已提交「确认并启动 SOP」，正在执行，结果会回复在会话里", DraftActionCancel: "已取消，没有执行"}[ev.Action]
	card := s.draftCardByID(draft)
	card.Done(status)
	return InboundAck{Accepted: inserted, InteractionID: item.ID, Toast: "已收到", Card: card}, nil
}

// tenantGate enforces the enterprise scope of new channels. A locked scope
// must match; an unlocked one records the candidate (without bumping the
// configuration version) and answers with a fixed notice. gated=true with an
// empty notice means drop the event.
func (s *Service) tenantGate(bot domain.NotificationBot, ev InboundEvent) (string, bool, error) {
	if bot.TenantState == domain.BotTenantLocked {
		if ev.TenantKey == "" || ev.TenantKey != bot.TenantKey {
			return "", true, nil
		}
		return "", false, nil
	}
	if ev.TenantKey == "" {
		return "", true, nil
	}
	state, err := s.Store.RecordBotTenantCandidate(bot.ID, ev.TenantKey, ev.EventID, bot.CredentialVersion)
	if err != nil {
		return "", true, err
	}
	if state == domain.BotTenantLocked {
		// Locked concurrently: apply the locked rule to the current value.
		current, err := s.Store.NotificationBot(bot.ID)
		if err != nil {
			return "", true, err
		}
		if current.TenantKey == ev.TenantKey {
			return "", false, nil
		}
		return "", true, nil
	}
	return noticeTenantPending, true, nil
}

// enqueueNotice persists a fixed reply for an event the model must not see.
// Only type and length are kept, never the original content.
func (s *Service) enqueueNotice(item domain.BotInteraction, ev InboundEvent, notice string) (InboundAck, error) {
	item.Action = actionSystemNotice
	item.Content = notice
	item.Quote = ""
	inserted, err := s.Store.EnqueueBotInteraction(item, ev.ChatType, ev.ChatID)
	if err != nil {
		return InboundAck{}, err
	}
	return InboundAck{Accepted: inserted, InteractionID: item.ID}, nil
}

func (s *Service) ignore(ev InboundEvent, reason string) InboundAck {
	log.Printf("%s inbound ignored reason=%s", channelLabel(ev.Channel), reason)
	return InboundAck{Ignored: reason}
}

func channelLabel(kind string) string {
	if kind == "" {
		return ChannelWeCom
	}
	return kind
}

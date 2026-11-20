package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"log"
	"strings"
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
		s.Store.NoteBotConnectionEvent(bot.LeaseKey(), ev.ChatType == "group")
		if ev.ChatType != "single" {
			return s.ignore(ev, "group_not_supported"), nil
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
	inserted, err := s.Store.EnqueueBotInteraction(item, ev.ChatType, ev.ChatID)
	if err != nil {
		log.Printf("%s event enqueue failed action=%s", channelLabel(ev.Channel), item.Action)
		return InboundAck{}, err
	}
	return InboundAck{Accepted: inserted, InteractionID: item.ID}, nil
}

// acceptCardAction queues a Feishu or DingTalk action card click. Only the
// recipient of the notification is placed in its conversation; anyone else
// lands in a conversation of their own and the action is refused as expired
// (NotificationCardTask checks the conversation against the delivery). The
// result goes out as a private message to the clicker.
func (s *Service) acceptCardAction(bot domain.NotificationBot, item domain.BotInteraction, ev InboundEvent) (InboundAck, error) {
	if !pushCardKey(ev.Action) || ev.DeliveryID == "" {
		return s.ignore(ev, "unknown_action"), nil
	}
	chat := ev.SenderID
	if delivery, err := s.Store.NotificationDelivery(ev.DeliveryID); err == nil && delivery.BotID == bot.ID && delivery.ProjectID == bot.ProjectID && delivery.TargetType == "single" && delivery.TargetID == ev.SenderID {
		chat = delivery.TargetID
	}
	item.Content, item.Quote, item.ContentLength = "", "", 0
	item.SendRoute = repo.BotSendRoutePush
	inserted, err := s.Store.EnqueueBotInteraction(item, "single", chat)
	if err != nil {
		return InboundAck{}, err
	}
	return InboundAck{Accepted: inserted, InteractionID: item.ID}, nil
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

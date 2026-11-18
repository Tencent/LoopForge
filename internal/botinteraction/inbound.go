package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"log"
	"strings"
)

const unsupportedMessageText = "[不支持的消息类型，请发送文字]"

// identityScope is the namespace for platform identities of one Bot:
// bindings and platform message de-duplication. Stage A only connects WeCom,
// whose historical scope is the external Bot ID. Stage B moves this to
// domain.NotificationBot.IdentityScope() and returns the config ID for new
// channels.
func identityScope(bot domain.NotificationBot) string {
	return bot.BotID
}

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
	case strings.TrimSpace(ev.Text) == "" && ev.Action == "":
		return domain.BotInteraction{}, "empty"
	}
	return domain.BotInteraction{
		ID:         repo.BotMessageID(identityScope(bot), ev.MessageID),
		BotID:      bot.ID,
		SenderID:   ev.SenderID,
		RequestID:  ev.Route.RequestID,
		Content:    ev.Text,
		Quote:      ev.Quote,
		Action:     ev.Action,
		DeliveryID: ev.DeliveryID,
		CardTaskID: ev.CardTaskID,
	}, ""
}

// accept is the common inbound sink: validate, classify and enqueue in one
// bounded transaction. Duplicates return Accepted=false without error so the
// adapter acknowledges them; storage errors are returned for redelivery.
func (s *Service) accept(ctx context.Context, bot domain.NotificationBot, ev InboundEvent) (InboundAck, error) {
	item, reason := interactionFromEvent(bot, ev)
	if reason != "" {
		log.Printf("%s inbound ignored reason=%s", channelLabel(ev.Channel), reason)
		return InboundAck{Ignored: reason}, nil
	}
	inserted, err := s.Store.EnqueueBotInteraction(item, ev.ChatType, ev.ChatID)
	if err != nil {
		log.Printf("%s event enqueue failed action=%s", channelLabel(ev.Channel), item.Action)
		return InboundAck{}, err
	}
	return InboundAck{Accepted: inserted, InteractionID: item.ID}, nil
}

func channelLabel(kind string) string {
	if kind == "" {
		return ChannelWeCom
	}
	return kind
}

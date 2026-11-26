package httpapi

import (
	"agentflow/internal/domain"
	"context"
	"log"
	"strings"
)

// botChatActor is the IM identity that asked for a platform write made on
// behalf of a chat turn. Authorization and execution always use the platform
// account the sender is bound to (botExecutionUser); this only names who in
// the IM asked, so audit trails can show it instead of only the account.
//
// It is carried in the request context of the internal platform calls that a
// Bot turn makes, next to requestUserContextKey. Unlike BotChatOrigin (which
// routes a run's notifications back to its conversation) it is attribution
// only and is set on every write path of a chat turn, including the ones
// without a conversation row to route to.
type botChatActor struct {
	// Channel is a domain.BotChannel* kind.
	Channel string
	// SenderID is the sender ID within that channel (for WeCom, the
	// enterprise-internal userid of the member).
	SenderID string
	// SenderName is the display name the channel reported for the sender,
	// empty on channels that report none (WeCom, Feishu).
	SenderName string
}

type botChatActorKey struct{}

// withBotChatActor carries the IM sender of the current chat turn. An empty
// sender leaves the context unchanged, so platform-initiated calls keep no
// IM attribution.
func withBotChatActor(ctx context.Context, channel, senderID, senderName string) context.Context {
	senderID = strings.TrimSpace(senderID)
	if ctx == nil || senderID == "" {
		return ctx
	}
	kind := domain.NotificationBot{Channel: channel}.ChannelKind()
	return context.WithValue(ctx, botChatActorKey{}, botChatActor{Channel: kind, SenderID: senderID, SenderName: strings.TrimSpace(senderName)})
}

// botChatActorFrom reports the IM sender behind the current call, if any.
func botChatActorFrom(ctx context.Context) (botChatActor, bool) {
	if ctx == nil {
		return botChatActor{}, false
	}
	actor, ok := ctx.Value(botChatActorKey{}).(botChatActor)
	if !ok || strings.TrimSpace(actor.SenderID) == "" {
		return botChatActor{}, false
	}
	return actor, true
}

// botSenderName is the best display name known for a sender of bot.
//
// reported is the name carried by the message being handled; DingTalk is the
// only channel that sends one. It is cached on the binding so later actions
// of the same sender that carry no name - card clicks, confirmations on the
// platform page, a LOOP started right after a clarification - can still be
// attributed to a person. WeCom and Feishu bot callbacks carry the sender ID
// alone, so this stays empty for them and the sender keeps being named by the
// platform account it is bound to.
func (h *assistantHandlers) botSenderName(bot domain.NotificationBot, senderID, reported string) string {
	reported = strings.TrimSpace(reported)
	if h == nil || h.repo == nil || strings.TrimSpace(senderID) == "" {
		return reported
	}
	if reported != "" {
		if err := h.repo.NoteBotSenderName(bot.IdentityScope(), senderID, reported); err != nil {
			// Attribution is best effort: a failed cache write must not fail
			// the turn, but it should not be silent either.
			log.Printf("bot sender name of %s not cached: %v", bot.ID, err)
		}
		return reported
	}
	binding, err := h.repo.BotUserBinding(bot.IdentityScope(), senderID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(binding.SenderName)
}

// botSenderNote names the IM sender in an engine decision note (node
// approvals, retries, answers). The sender ID stays because it is the stable
// identity; a known display name is added for readability.
func (h *assistantHandlers) botSenderNote(bot domain.NotificationBot, item domain.BotInteraction) string {
	if name := h.botSenderName(bot, item.SenderID, item.SenderName); name != "" {
		return item.SenderID + "（" + name + "）"
	}
	return item.SenderID
}

package httpapi

import (
	"agentflow/internal/domain"
	"context"
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
}

type botChatActorKey struct{}

// withBotChatActor carries the IM sender of the current chat turn. An empty
// sender leaves the context unchanged, so platform-initiated calls keep no
// IM attribution.
func withBotChatActor(ctx context.Context, channel, senderID string) context.Context {
	senderID = strings.TrimSpace(senderID)
	if ctx == nil || senderID == "" {
		return ctx
	}
	kind := domain.NotificationBot{Channel: channel}.ChannelKind()
	return context.WithValue(ctx, botChatActorKey{}, botChatActor{Channel: kind, SenderID: senderID})
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

// botChannelKindName is the user-facing IM product name of a channel kind.
func botChannelKindName(kind string) string {
	return botChannelName(domain.NotificationBot{Channel: kind})
}

// Label is how the sender is shown in an audit trail, e.g. "企业微信 wangxx".
func (a botChatActor) Label() string {
	return botChannelKindName(a.Channel) + " " + strings.TrimSpace(a.SenderID)
}

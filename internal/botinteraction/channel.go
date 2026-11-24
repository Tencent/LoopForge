package botinteraction

import (
	"agentflow/internal/domain"
	"context"
	"errors"
	"time"
)

// Channel kinds. Stage A only connects WeCom; Feishu and DingTalk adapters are
// added in later stages and report ErrChannelNotReady until then.
const (
	ChannelWeCom    = "wecom"
	ChannelFeishu   = "feishu"
	ChannelDingTalk = "dingtalk"
)

// ErrChannelNotReady is returned for a channel kind without a registered or
// fully configured adapter. HTTP handlers map it to `channel_not_ready`.
var ErrChannelNotReady = errors.New("channel_not_ready")

// Credential is the decrypted secret material for one Bot configuration.
type Credential struct {
	Secret string
}

// Verification is what an adapter learns while checking credentials. Tenant
// fields are informational; tenant locking happens in the confirmation flow.
type Verification struct {
	TenantKey  string
	TenantName string
}

// ReplyRoute holds the channel-specific data needed to answer in the original
// conversation. It is persisted encrypted once later stages store it.
type ReplyRoute struct {
	RequestID string    // WeCom callback req_id used by stream replies
	MessageID string    // Feishu original message_id for Message Reply
	Webhook   string    // DingTalk sessionWebhook (authorization-bearing)
	ExpiresAt time.Time // zero when the route does not expire
}

// InboundEvent is a channel-neutral message or card event. Adapters fill it
// only from trusted, authenticated connection data; the service decides the
// project, model and permissions from the Bot configuration of the connection.
type InboundEvent struct {
	Channel   string
	AppID     string // external application identity carried by the event
	TenantKey string // enterprise scope carried by the event, when available
	EventID   string // platform delivery/event id, kept for diagnostics
	MessageID string // platform business message id, used for de-duplication

	SenderID string
	ChatID   string
	ChatType string // "single" or "group"
	MsgType  string
	Text     string
	Quote    string
	// Mentioned is set for group messages that @ the Bot (Feishu,
	// DingTalk); other group messages are ignored.
	Mentioned bool
	// Option is the value picked in a card drop-down (Feishu), or the
	// reference carried by a clarification card button.
	Option string
	// CardChatID is the chat a card was clicked in (Feishu open_chat_id),
	// when the platform reports it.
	CardChatID string
	// ParentID is the message a text message replies to (Feishu quote).
	ParentID string
	// Selections are the drop-downs submitted with a WeCom card
	// (question key → option).
	Selections map[string]string

	// WeCom card callbacks.
	Action     string
	DeliveryID string
	CardTaskID string

	Route ReplyRoute
}

// InboundAck reports the persistence outcome of one inbound event.
// Accepted is true only when this event created a new interaction; duplicates
// and ignored events return Accepted=false with a nil error.
type InboundAck struct {
	Accepted      bool
	InteractionID string
	Ignored       string // reason when the event was dropped as invalid
	// Card and Toast answer a card callback: the updated card (buttons
	// replaced by a status, or the new selection) and a short notice.
	Card  *PushCard
	Toast string
}

// InboundSink persists an event. A non-nil error means the event was not
// stored and the adapter must negative-acknowledge so the platform redelivers.
type InboundSink func(context.Context, InboundEvent) (InboundAck, error)

// OutboundMessage is one reply or push. Text is final user-visible content.
type OutboundMessage struct {
	InteractionID string // interaction or delivery id
	Action        string // WeCom card action of the source interaction, if any
	ChatID        string
	ChatType      string
	Text          string
	SendKey       string // persisted idempotency key (stage C)
	Route         ReplyRoute
	// Card, when set on a Feishu or DingTalk push, is sent as an action
	// card; Text stays the plain-text fallback.
	Card *PushCard
}

// SendOutcome classifies a send by delivery evidence, not by error type.
type SendOutcome string

const (
	// Delivered: the platform confirmed this message.
	Delivered SendOutcome = "delivered"
	// NotDelivered: evidence shows the request was never submitted, or the
	// platform explicitly reported it as not sent and retryable.
	NotDelivered SendOutcome = "not_delivered"
	// Rejected: the platform explicitly refused the message.
	Rejected SendOutcome = "rejected"
	// Unknown: the request may have been written; delivery cannot be proven.
	Unknown SendOutcome = "unknown"
)

type SendResult struct {
	Outcome           SendOutcome
	Code              string
	Retryable         bool
	PlatformMessageID string
	SafeDetail        string // user/admin visible, never contains secrets
}

func (r SendResult) err() error {
	if r.SafeDetail != "" {
		return errors.New(r.SafeDetail)
	}
	return errors.New("回复发送失败：" + string(r.Outcome))
}

// Channel is one IM platform adapter.
type Channel interface {
	Kind() string
	// Verify checks credentials without opening a receiving connection, so it
	// never displaces the connection currently held by a receiver.
	Verify(context.Context, domain.NotificationBot, Credential) (Verification, error)
	// Connect opens a receiving connection. Inbound events are passed to sink.
	Connect(context.Context, domain.NotificationBot, Credential, InboundSink) (Conn, error)
}

// Conn is one live receiving connection.
type Conn interface {
	// Done is closed when the connection is no longer usable.
	Done() <-chan struct{}
	Reply(context.Context, OutboundMessage) SendResult
	Close() error
}

// PushSender sends proactive notifications through application APIs. It is
// implemented by the channel object and does not require a receiving Conn.
type PushSender interface {
	Push(context.Context, domain.NotificationBot, Credential, OutboundMessage) SendResult
}

// progressConn is an optional Conn capability: in-place progress updates for
// a reply that is still being generated (WeCom stream replies).
type progressConn interface {
	progressTurn(context.Context, domain.BotInteraction) (context.Context, func())
}

// cardConn is an optional Conn capability: interactive notification cards.
type cardConn interface {
	sendCard(ctx context.Context, chatID string, card map[string]any) SendResult
	updateActionCard(ctx context.Context, item domain.BotInteraction, result string, finish bool) error
}

package domain

import "time"

// BotConnection is the receiving lease of one application. BotID holds the
// lease key (NotificationBot.LeaseKey): the external Bot ID for WeCom, the
// receive slot for other channels. LeaseUntilUS and the epoch are written and
// compared on the database clock; LeaseUntil is a display copy only.
type BotConnection struct {
	BotID        string    `json:"botId" gorm:"primaryKey;size:191"`
	Owner        string    `json:"-" gorm:"size:64"`
	LeaseUntil   time.Time `json:"-"`
	LeaseUntilUS int64     `json:"-" gorm:"not null;default:0"`
	// Epoch increments whenever a different owner takes the lease.
	Epoch                int64      `json:"epoch" gorm:"not null;default:0"`
	OwnerChanges         int64      `json:"ownerChanges" gorm:"not null;default:0"`
	LastEventAt          *time.Time `json:"lastEventAt,omitempty"`
	AuthFailures         int64      `json:"authFailures" gorm:"not null;default:0"`
	IgnoredGroupMessages int64      `json:"ignoredGroupMessages" gorm:"not null;default:0"`
	State                string     `json:"state" gorm:"size:32"`
	LastError            string     `json:"lastError,omitempty" gorm:"size:255"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

// Interaction statuses (bot_interactions.status).
const (
	BotQueued       = "queued"
	BotRunning      = "running"
	BotReady        = "ready"
	BotSending      = "sending"
	BotReplied      = "replied"
	BotReplyFailed  = "reply_failed"
	BotReplyUnknown = "reply_unknown"
	BotCancelled    = "cancelled"
)

// Terminal reasons for queued notifications refused before the platform call.
const (
	BotTerminalTargetUnavailable   = "target_unavailable"
	BotTerminalNotificationExpired = "notification_expired"
)

type BotConversation struct {
	ID        string    `json:"id" gorm:"primaryKey;size:64"`
	BotID     string    `json:"botId" gorm:"index;size:191"`
	ChatID    string    `json:"chatId" gorm:"size:191"`
	ChatType  string    `json:"chatType" gorm:"size:16"`
	SessionID string    `json:"sessionId" gorm:"size:64"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type BotInteraction struct {
	ID             string    `json:"id" gorm:"primaryKey;size:64"`
	BotID          string    `json:"botId" gorm:"index:idx_bot_queue,priority:1;index:idx_bot_send_due,priority:1;size:191"`
	ConversationID string    `json:"conversationId" gorm:"index;size:64"`
	SessionID      string    `json:"sessionId" gorm:"index;size:64"`
	SenderID       string    `json:"senderId" gorm:"size:191"`
	RequestID      string    `json:"-" gorm:"size:191"`
	Content        string    `json:"content" gorm:"type:text"`
	Quote          string    `json:"quote,omitempty" gorm:"type:text"`
	Action         string    `json:"action,omitempty" gorm:"size:32"`
	CardTaskID     string    `json:"cardTaskId,omitempty" gorm:"size:128"`
	DeliveryID     string    `json:"deliveryId,omitempty" gorm:"size:64"`
	Status         string    `json:"status" gorm:"index:idx_bot_queue,priority:2;index:idx_bot_send_due,priority:2;size:32"`
	Reply          string    `json:"reply,omitempty" gorm:"type:text"`
	LastError      string    `json:"lastError,omitempty" gorm:"size:255"`
	CreatedAt      time.Time `json:"createdAt" gorm:"index:idx_bot_queue,priority:3"`
	UpdatedAt      time.Time `json:"updatedAt"`

	// ProjectID snapshots the configuration's project for record access
	// after the Bot is deleted.
	ProjectID string `json:"projectId,omitempty" gorm:"size:191;index"`
	// Lease fencing: owner and epoch of the connection that claimed the item.
	LeaseOwner    string `json:"-" gorm:"size:64"`
	LeaseEpoch    int64  `json:"-" gorm:"not null;default:0"`
	ConfigVersion int64  `json:"configVersion,omitempty" gorm:"not null;default:0"`
	// Platform evidence.
	PlatformEventID   string `json:"platformEventId,omitempty" gorm:"size:191"`
	PlatformMessageID string `json:"platformMessageId,omitempty" gorm:"size:191"`
	MsgType           string `json:"msgType,omitempty" gorm:"size:32"`
	ContentLength     int    `json:"contentLength,omitempty" gorm:"not null;default:0"`
	// ReplyRoute is channel routing data (sealed when it carries authority);
	// RouteExpiresAtUS is 0 when the route does not expire.
	ReplyRoute       string `json:"-" gorm:"type:text"`
	RouteExpiresAtUS int64  `json:"-" gorm:"not null;default:0"`
	// Delivery state machine. SendKey is fixed per generation; retries reuse
	// it. Times are database-clock epoch microseconds.
	SendKey            string `json:"-" gorm:"size:64"`
	DeliveryGeneration int64  `json:"deliveryGeneration" gorm:"not null;default:0"`
	SendStartedAtUS    int64  `json:"-" gorm:"not null;default:0"`
	SendAttempts       int    `json:"sendAttempts" gorm:"not null;default:0"`
	UndeliveredRetries int    `json:"-" gorm:"not null;default:0"`
	NextSendAtUS       int64  `json:"-" gorm:"index:idx_bot_send_due,priority:3;not null;default:0"`
	SendLeaseUntilUS   int64  `json:"-" gorm:"not null;default:0"`
	// Manual handling and audit.
	ResentBy       int64      `json:"resentBy,omitempty" gorm:"not null;default:0"`
	ConfirmedBy    int64      `json:"confirmedBy,omitempty" gorm:"not null;default:0"`
	ConfirmedAt    *time.Time `json:"confirmedAt,omitempty"`
	ReceiptSource  string     `json:"receiptSource,omitempty" gorm:"size:16"`
	TerminalReason string     `json:"terminalReason,omitempty" gorm:"size:64"`
	// SendRoute is empty for replies in the original conversation,
	// "push" after an administrator switched an expired DingTalk reply to
	// a proactive private message (stage G), and "push_chat" for messages
	// the platform sends into the conversation on its own (group results
	// of card clicks, clarification questions).
	SendRoute string `json:"sendRoute,omitempty" gorm:"size:16"`
	// ReplyTo is the platform message this message replies to (Feishu
	// quote), used to recognise answers to clarification cards.
	ReplyTo string `json:"-" gorm:"size:191"`
	// PushResendAvailable is computed for the records page: the reply
	// expired and the original sender is a verified push target.
	PushResendAvailable bool   `json:"pushResendAvailable,omitempty" gorm:"-"`
	LastActionKey       string `json:"-" gorm:"size:64"`
	// BodyPurgedAt is set when the retention cleanup removed the message
	// and reply text (stage H); the row stays until the record retention
	// ends and can no longer be sent again.
	BodyPurgedAt *time.Time `json:"bodyPurgedAt,omitempty"`
}

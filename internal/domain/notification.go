package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

type BotChat struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	LastMessageTime string `json:"lastMessageTime,omitempty"`
	// VerifiedAt is set for Feishu/DingTalk targets after a trial
	// proactive send succeeded (target_verified_at of the bound sender).
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
}
type NotificationBot struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	OwnerID   int64  `json:"ownerId"`
	Model     string `json:"model,omitempty"`
	Name      string `json:"name"`
	// Channel is wecom, feishu or dingtalk. Empty means a legacy WeCom record.
	Channel string `json:"channel,omitempty"`
	// BotID is the external application identity: the WeCom Bot ID, Feishu
	// App ID or DingTalk Client ID.
	BotID              string     `json:"botId"`
	InteractionEnabled bool       `json:"interactionEnabled"`
	Enabled            bool       `json:"enabled"`
	SecretConfigured   bool       `json:"secretConfigured"`
	Target             *BotChat   `json:"target,omitempty"`
	Health             string     `json:"health"`
	LastCheckedAt      *time.Time `json:"lastCheckedAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`

	// WriteApproval is readonly, auto or confirm (WeCom only). Empty means a legacy WeCom record,
	// which keeps the automatic execution it had before channels existed.
	WriteApproval string `json:"writeApproval,omitempty"`
	// CardTemplateID is the DingTalk interactive card template used for
	// action cards of waiting or failed nodes. Empty sends plain text.
	CardTemplateID string `json:"cardTemplateId,omitempty"`
	// ConfigVersion increments on every saved change; CredentialVersion only
	// when the secret changes. Both mirror physical columns.
	ConfigVersion     int64 `json:"configVersion"`
	CredentialVersion int64 `json:"credentialVersion"`
	// CredentialFormat reports whether the stored secret is still legacy
	// plaintext ("plaintext") or sealed ("encrypted"). Filled on read.
	CredentialFormat string `json:"credentialFormat,omitempty"`

	// Enterprise scope (Feishu tenant_key, DingTalk CorpId). TenantState is
	// awaiting_confirmation, locked or conflict; empty for WeCom.
	TenantKey         string           `json:"tenantKey,omitempty"`
	TenantName        string           `json:"tenantName,omitempty"`
	TenantState       string           `json:"tenantState,omitempty"`
	TenantCandidate   *TenantCandidate `json:"tenantCandidate,omitempty"`
	TenantConfirmedBy int64            `json:"tenantConfirmedBy,omitempty"`
	TenantConfirmedAt *time.Time       `json:"tenantConfirmedAt,omitempty"`
	// TenantExpected is the enterprise scope an administrator entered
	// (DingTalk CorpId). The first trusted event that matches it locks the
	// scope automatically; a different value moves the state to conflict.
	TenantExpected   string `json:"tenantExpected,omitempty"`
	TenantExpectedBy int64  `json:"tenantExpectedBy,omitempty"`
}

// TenantCandidate is the enterprise scope observed on the first trusted
// event of an authenticated connection, awaiting administrator confirmation.
type TenantCandidate struct {
	TenantKey         string    `json:"tenantKey"`
	EventID           string    `json:"eventId,omitempty"`
	ReceivedAt        time.Time `json:"receivedAt"`
	CredentialVersion int64     `json:"credentialVersion"`
	Conflict          string    `json:"conflict,omitempty"`
}

const (
	BotChannelWeCom    = "wecom"
	BotChannelFeishu   = "feishu"
	BotChannelDingTalk = "dingtalk"

	BotWriteReadOnly = "readonly"
	BotWriteAuto     = "auto"
	// BotWriteConfirm (WeCom, stage G): write drafts are confirmed by the
	// bound user on the platform page before they run.
	BotWriteConfirm = "confirm"

	BotTenantAwaiting = "awaiting_confirmation"
	BotTenantLocked   = "locked"
	BotTenantConflict = "conflict"
)

// ChannelKind returns the normalized channel; legacy records are WeCom.
func (b NotificationBot) ChannelKind() string {
	switch strings.TrimSpace(b.Channel) {
	case BotChannelFeishu:
		return BotChannelFeishu
	case BotChannelDingTalk:
		return BotChannelDingTalk
	default:
		return BotChannelWeCom
	}
}

// IdentityScope namespaces platform identities (bindings, message
// de-duplication). WeCom keeps its historical external Bot ID; new channels
// use the immutable configuration ID because their user IDs are app-scoped.
func (b NotificationBot) IdentityScope() string {
	if b.ChannelKind() == BotChannelWeCom {
		return b.BotID
	}
	return b.ID
}

// ReceiveSlot identifies one receiving application within a deployment.
// Only enabled interactive configurations occupy it.
func (b NotificationBot) ReceiveSlot() string {
	sum := sha256.Sum256([]byte(b.BotID))
	return b.ChannelKind() + ":" + hex.EncodeToString(sum[:])
}

// LeaseKey is the bot_connections.bot_id of the receiving connection. WeCom
// keeps the external Bot ID so existing rows and running peers stay valid.
func (b NotificationBot) LeaseKey() string {
	if b.ChannelKind() == BotChannelWeCom {
		return b.BotID
	}
	return b.ReceiveSlot()
}

// EffectiveWriteApproval maps legacy WeCom records without the field to the
// automatic execution they always had.
func (b NotificationBot) EffectiveWriteApproval() string {
	switch b.WriteApproval {
	case BotWriteReadOnly, BotWriteAuto, BotWriteConfirm:
		return b.WriteApproval
	}
	if b.ChannelKind() == BotChannelWeCom {
		return BotWriteAuto
	}
	return BotWriteReadOnly
}

// ConnectionFingerprint covers only the fields that require reconnecting.
// Names, models, tenant locking and write policy are read per event/turn.
func (b NotificationBot) ConnectionFingerprint() string {
	return strings.Join([]string{b.ChannelKind(), b.BotID, strconv.FormatInt(b.CredentialVersion, 10), boolFlag(b.Enabled), boolFlag(b.InteractionEnabled)}, "|")
}

func boolFlag(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

type NotificationBinding struct {
	BotID         string   `json:"botId"`
	Events        []string `json:"events"` // succeeded, failed, waiting
	Template      string   `json:"template,omitempty"`
	Scope         string   `json:"scope,omitempty"`       // node (default), run
	ContentMode   string   `json:"contentMode,omitempty"` // template (default), ai
	AIInstruction string   `json:"aiInstruction,omitempty"`
	WaitingPolicy string   `json:"waitingPolicy,omitempty"`
}
type NodeNotification = NotificationBinding

const DefaultNotificationReportModel = "default"

type NotificationReportRequest struct {
	Binding NotificationBinding
	Context NotificationContext
}

type NotificationDelivery struct {
	ReportRequest string `json:"-" gorm:"type:text"`
	ReportState   string `json:"reportState,omitempty" gorm:"size:32"`
	ReportModel   string `json:"reportModel,omitempty" gorm:"size:191"`
	ReportError   string `json:"reportError,omitempty" gorm:"size:255"`

	WaitingPolicy string     `json:"waitingPolicy,omitempty" gorm:"size:32;default:actionable"`
	SourceType    string     `json:"sourceType" gorm:"size:32;default:run;index:idx_notification_source,priority:1"`
	SourceID      string     `json:"sourceId,omitempty" gorm:"size:191;index:idx_notification_source,priority:2"`
	SourceName    string     `json:"sourceName,omitempty" gorm:"size:255"`
	ContentSource string     `json:"contentSource,omitempty" gorm:"size:32"`
	ID            string     `json:"id" gorm:"primaryKey;size:64"`
	ProjectID     string     `json:"projectId" gorm:"size:191;index"`
	BotID         string     `json:"botId" gorm:"size:191;index"`
	RunID         string     `json:"runId,omitempty" gorm:"size:191"`
	NodeID        string     `json:"nodeId,omitempty" gorm:"size:191"`
	EventType     string     `json:"eventType" gorm:"size:64"`
	TargetID      string     `json:"-" gorm:"type:text"`
	TargetType    string     `json:"targetType,omitempty" gorm:"size:16"`
	TargetName    string     `json:"targetName" gorm:"size:255"`
	Content       string     `json:"-" gorm:"type:text"`
	Status        string     `json:"status" gorm:"size:40;index:idx_notification_due,priority:1"`
	Attempts      int        `json:"attempts"`
	Attempt       int        `json:"-"`
	ReworkCount   int        `json:"-"`
	NextAttemptAt time.Time  `json:"nextAttemptAt" gorm:"index:idx_notification_due,priority:2"`
	LeaseUntil    *time.Time `json:"-"`
	LeaseToken    string     `json:"-" gorm:"size:64"`
	LastError     string     `json:"lastError,omitempty" gorm:"size:255"`
	CreatedAt     time.Time  `json:"createdAt"`
	SentAt        *time.Time `json:"sentAt,omitempty"`

	// Proactive sends of Feishu and DingTalk (stage G). SendKey is the
	// persisted idempotency key of the current generation (Feishu uuid);
	// SendStartedAtUS is the database time the first attempt of this
	// generation started, 0 while nothing may have been sent.
	SendKey            string     `json:"-" gorm:"size:64"`
	DeliveryGeneration int        `json:"deliveryGeneration,omitempty" gorm:"not null;default:0"`
	SendStartedAtUS    int64      `json:"-" gorm:"not null;default:0"`
	PlatformMessageID  string     `json:"-" gorm:"size:191"`
	ConfirmedBy        int64      `json:"confirmedBy,omitempty"`
	ConfirmedAt        *time.Time `json:"confirmedAt,omitempty"`
	ReceiptSource      string     `json:"receiptSource,omitempty" gorm:"size:16"`
	// Origin is "chat" for deliveries sent back to the conversation a run
	// was started from (no SOP binding needed); TargetType is then "group"
	// for a group chat.
	Origin string `json:"origin,omitempty" gorm:"size:16"`
	// OriginUserID is the account bound to the private chat when the run started.
	OriginUserID int64 `json:"-" gorm:"not null;default:0"`
}

func (NotificationDelivery) TableName() string { return "notification_deliveries" }

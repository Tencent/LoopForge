package domain

import "time"

// BotUserBinding is scoped to the external Bot ID; do not assume user IDs
// obtained from different enterprises or Bot namespaces are interchangeable.
type BotUserBinding struct {
	ID string `json:"id" gorm:"primaryKey;size:64"`
	// BotID is the identity scope (NotificationBot.IdentityScope): the
	// external WeCom Bot ID, or the configuration ID for other channels.
	BotID string `json:"botId" gorm:"size:191;index"`
	// ConfigID is the owning configuration; required for new channels and
	// backfilled for WeCom by migration 037 where ownership is unambiguous.
	ConfigID string `json:"configId,omitempty" gorm:"size:191;index"`
	Channel  string `json:"channel,omitempty" gorm:"size:16"`
	BotName  string `json:"botName,omitempty" gorm:"-"`
	// Link issuance rate limit: GuideCount links within the window that
	// started at GuideWindowStart; LastLinkAt limits explicit requests.
	GuideWindowStart *time.Time `json:"-"`
	GuideCount       int        `json:"-" gorm:"not null;default:0"`
	LastLinkAt       *time.Time `json:"-"`
	SenderID         string     `json:"senderId" gorm:"size:191"`
	// TargetVerifiedAt records a successful trial proactive send to this
	// sender (Feishu/DingTalk notification target). Cleared on revocation.
	TargetVerifiedAt *time.Time `json:"targetVerifiedAt,omitempty"`
	UserID           int64      `json:"userId" gorm:"index"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}
type BotBindingChallenge struct {
	ID         string    `json:"-" gorm:"primaryKey;size:64"`
	BindingID  string    `json:"-" gorm:"index;size:64"`
	ConfigID   string    `json:"-" gorm:"size:191;index"`
	Channel    string    `json:"-" gorm:"size:16"`
	SenderMask string    `json:"-" gorm:"size:64"`
	UserID     int64     `json:"-"`
	CodeHash   string    `json:"-" gorm:"size:64"`
	Status     string    `json:"-" gorm:"size:16"`
	Attempts   int       `json:"-"`
	ExpiresAt  time.Time `json:"-" gorm:"index"`
	CreatedAt  time.Time `json:"-"`
}

// BotPendingDraft is a frozen set of assistant write drafts from a WeCom
// Bot in the confirm write mode, awaiting the bound user's confirmation on
// the platform page. Drafts is the JSON the assistant produced.
type BotPendingDraft struct {
	ID            string     `json:"id"`
	ConfigID      string     `json:"configId"`
	IdentityScope string     `json:"-"`
	SenderID      string     `json:"-"`
	ProjectID     string     `json:"projectId"`
	UserID        int64      `json:"-"`
	InteractionID string     `json:"-"`
	Summary       string     `json:"summary"`
	Drafts        string     `json:"-"`
	Status        string     `json:"status"`
	ExecKey       string     `json:"-"`
	Result        string     `json:"result,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	ExecutedAt    *time.Time `json:"executedAt,omitempty"`
}

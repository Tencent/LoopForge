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
	// SOP is the SOP choice offered on the chat card when the draft
	// creates exactly one requirement (nil otherwise); Choice is what the
	// requester picked there: a workflow ID, BotDraftDefaultSOP, or empty
	// when nothing was picked.
	SOP    *BotDraftSOP `json:"-"`
	Choice string       `json:"-"`
	// RepoChoice is the repository picked on the chat card: a repository
	// ID, BotDraftDefaultSOP (the project default), or empty.
	RepoChoice string `json:"-"`
}

// BotDraftDefaultSOP is the card choice "project default SOP".
const BotDraftDefaultSOP = "__default__"

// BotDraftSOP is the SOP selection of a new requirement in a draft.
type BotDraftSOP struct {
	Index       int                 `json:"index"`
	WorkflowID  string              `json:"workflowId,omitempty"` // named by the model
	DefaultName string              `json:"defaultName"`
	Workflows   []BotDraftSOPOption `json:"workflows"`
	// The repository of the new requirement: the one the model named, the
	// project default's name (empty when the project has none) and the
	// project's repositories.
	RepositoryID          string              `json:"repositoryId,omitempty"`
	DefaultRepositoryName string              `json:"defaultRepositoryName,omitempty"`
	Repositories          []BotDraftSOPOption `json:"repositories,omitempty"`
}

// AllowsRepository reports whether a repository may be chosen (the
// project default always may).
func (s *BotDraftSOP) AllowsRepository(id string) bool {
	if s == nil {
		return false
	}
	if id == BotDraftDefaultSOP {
		return true
	}
	for _, r := range s.Repositories {
		if r.ID == id {
			return true
		}
	}
	return false
}

// SelectedRepository is the effective repository selection of the card:
// the pick, else the repository the model named, else the project default.
func (d BotPendingDraft) SelectedRepository() string {
	switch {
	case d.RepoChoice != "":
		return d.RepoChoice
	case d.SOP != nil && d.SOP.RepositoryID != "":
		return d.SOP.RepositoryID
	}
	return BotDraftDefaultSOP
}

// BotDraftSOPOption is one selectable SOP.
type BotDraftSOPOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Allows reports whether id may be chosen (the default always may).
func (s *BotDraftSOP) Allows(id string) bool {
	if s == nil {
		return false
	}
	if id == BotDraftDefaultSOP {
		return true
	}
	for _, w := range s.Workflows {
		if w.ID == id {
			return true
		}
	}
	return false
}

// Selected is the effective card selection: the pick, else the SOP the
// model named, else the project default.
func (d BotPendingDraft) Selected() string {
	switch {
	case d.Choice != "":
		return d.Choice
	case d.SOP != nil && d.SOP.WorkflowID != "":
		return d.SOP.WorkflowID
	}
	return BotDraftDefaultSOP
}

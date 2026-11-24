package repo

import (
	"agentflow/internal/domain"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Pending write drafts of WeCom Bots in the "confirm" write mode (stage G).
// The assistant's drafts are frozen when the reply is produced; the bound
// user confirms them on /bot-approve.html. Confirmation atomically moves a
// draft from pending to executing, so a repeated click or a second tab can
// never execute it twice. A draft stuck in executing (process ended during
// the business writes) is reported as needs_review and never re-executed.

const (
	BotDraftPending     = "pending"
	BotDraftExecuting   = "executing"
	BotDraftExecuted    = "executed"
	BotDraftFailed      = "failed"
	BotDraftNeedsReview = "needs_review"
	BotDraftExpired     = "expired"
	BotDraftCancelled   = "cancelled"

	// BotDraftTTL is how long a draft can be confirmed.
	BotDraftTTL = 30 * time.Minute
	// botDraftExecutingStale: an executing draft older than this is
	// considered interrupted.
	botDraftExecutingStale = 10 * time.Minute
)

var (
	ErrBotDraftNotPending = errors.New("bot_draft_not_pending")
	ErrBotDraftTampered   = errors.New("草稿参数校验失败，已停止执行")
)

type botPendingDraftRow struct {
	ID            string `gorm:"primaryKey;size:64"`
	ConfigID      string `gorm:"size:191;index"`
	IdentityScope string `gorm:"size:191;index:idx_bot_draft_sender,priority:1"`
	SenderID      string `gorm:"size:191;index:idx_bot_draft_sender,priority:2"`
	ProjectID     string `gorm:"size:191"`
	UserID        int64  `gorm:"index"`
	InteractionID string `gorm:"size:64"`
	Summary       string `gorm:"type:text"`
	Drafts        string `gorm:"type:mediumtext"`
	ParamsHash    string `gorm:"size:64"`
	Status        string `gorm:"size:16;index"`
	ExecKey       string `gorm:"size:64"`
	Result        string `gorm:"type:text"`
	Reason        string `gorm:"size:255"`
	ExpiresAt     time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ExecutedAt    *time.Time
	// BodyPurgedAt: the retention cleanup removed the drafts, summary and
	// result (stage H).
	BodyPurgedAt *time.Time
	// SOPOptions (JSON domain.BotDraftSOP) and SOPChoice: the SOP picked on
	// the chat card for the draft's new requirement.
	SOPOptions string `gorm:"type:text"`
	SOPChoice  string `gorm:"size:191"`
	// RepoChoice: the repository picked on the chat card.
	RepoChoice string `gorm:"size:191"`
}

func (botPendingDraftRow) TableName() string { return "bot_pending_drafts" }

func (row botPendingDraftRow) draft() domain.BotPendingDraft {
	d := domain.BotPendingDraft{ID: row.ID, ConfigID: row.ConfigID, IdentityScope: row.IdentityScope, SenderID: row.SenderID, ProjectID: row.ProjectID, UserID: row.UserID, InteractionID: row.InteractionID, Summary: row.Summary, Drafts: row.Drafts, Status: row.Status, ExecKey: row.ExecKey, Result: row.Result, Reason: row.Reason, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ExecutedAt: row.ExecutedAt, Choice: row.SOPChoice, RepoChoice: row.RepoChoice}
	if row.SOPOptions != "" {
		var sop domain.BotDraftSOP
		if json.Unmarshal([]byte(row.SOPOptions), &sop) == nil {
			d.SOP = &sop
		}
	}
	return d
}

// SetBotPendingDraftChoice records the SOP the requester picked on the chat
// card. Only a pending draft of that sender and configuration, and only an
// offered SOP, is accepted.
func (r *AssistantRepo) SetBotPendingDraftChoice(id, configID, sender, choice string, now time.Time) (domain.BotPendingDraft, error) {
	var row botPendingDraftRow
	if err := r.DB.First(&row, "id = ?", id).Error; err != nil {
		return domain.BotPendingDraft{}, err
	}
	d := row.draft()
	if d.ConfigID != configID || d.SenderID != sender || d.Status != BotDraftPending || !now.Before(d.ExpiresAt) || !d.SOP.Allows(choice) {
		return d, ErrBotDraftNotPending
	}
	if err := r.DB.Model(&botPendingDraftRow{}).Where("id = ? AND status = ?", id, BotDraftPending).Update("sop_choice", choice).Error; err != nil {
		return d, err
	}
	d.Choice = choice
	return d, nil
}

// SetBotPendingDraftRepository records the repository the requester picked
// on the chat card; only an offered repository is accepted.
func (r *AssistantRepo) SetBotPendingDraftRepository(id, configID, sender, choice string, now time.Time) (domain.BotPendingDraft, error) {
	var row botPendingDraftRow
	if err := r.DB.First(&row, "id = ?", id).Error; err != nil {
		return domain.BotPendingDraft{}, err
	}
	d := row.draft()
	if d.ConfigID != configID || d.SenderID != sender || d.Status != BotDraftPending || !now.Before(d.ExpiresAt) || !d.SOP.AllowsRepository(choice) {
		return d, ErrBotDraftNotPending
	}
	if err := r.DB.Model(&botPendingDraftRow{}).Where("id = ? AND status = ?", id, BotDraftPending).Update("repo_choice", choice).Error; err != nil {
		return d, err
	}
	d.RepoChoice = choice
	return d, nil
}

func botDraftHash(drafts string) string {
	sum := sha256.Sum256([]byte(drafts))
	return hex.EncodeToString(sum[:])
}

// CreateBotPendingDraft freezes drafts for confirmation and returns the
// random draft ID used in the link.
func (r *AssistantRepo) CreateBotPendingDraft(d domain.BotPendingDraft, now time.Time) (domain.BotPendingDraft, error) {
	options := ""
	if d.SOP != nil {
		raw, err := json.Marshal(d.SOP)
		if err != nil {
			return domain.BotPendingDraft{}, err
		}
		options = string(raw)
	}
	row := botPendingDraftRow{SOPOptions: options, ID: randomHex(16), ConfigID: d.ConfigID, IdentityScope: d.IdentityScope, SenderID: d.SenderID, ProjectID: d.ProjectID, UserID: d.UserID, InteractionID: d.InteractionID, Summary: d.Summary, Drafts: d.Drafts, ParamsHash: botDraftHash(d.Drafts), Status: BotDraftPending, ExecKey: randomHex(16), ExpiresAt: now.Add(BotDraftTTL), CreatedAt: now, UpdatedAt: now}
	if err := r.DB.Create(&row).Error; err != nil {
		return domain.BotPendingDraft{}, err
	}
	return row.draft(), nil
}

// BotPendingDraft reads a draft, settling lazy transitions: an expired
// pending draft becomes expired, an executing draft past the stale window
// becomes needs_review.
func (r *AssistantRepo) BotPendingDraft(id string, now time.Time) (domain.BotPendingDraft, error) {
	var row botPendingDraftRow
	if err := r.DB.First(&row, "id = ?", id).Error; err != nil {
		return domain.BotPendingDraft{}, err
	}
	switch {
	case row.Status == BotDraftPending && !now.Before(row.ExpiresAt):
		r.DB.Model(&botPendingDraftRow{}).Where("id = ? AND status = ?", id, BotDraftPending).Updates(map[string]any{"status": BotDraftExpired, "reason": "草稿已过期", "updated_at": now})
		row.Status, row.Reason = BotDraftExpired, "草稿已过期"
	case row.Status == BotDraftExecuting && now.Sub(row.UpdatedAt) > botDraftExecutingStale:
		r.DB.Model(&botPendingDraftRow{}).Where("id = ? AND status = ?", id, BotDraftExecuting).Updates(map[string]any{"status": BotDraftNeedsReview, "reason": "执行过程中断，结果待核对；平台不会自动重新执行", "updated_at": now})
		row.Status, row.Reason = BotDraftNeedsReview, "执行过程中断，结果待核对；平台不会自动重新执行"
	}
	return row.draft(), nil
}

// BotPendingDraftForInteraction returns the pending draft a chat turn
// produced, if any (used to send the confirmation as a card).
func (r *AssistantRepo) BotPendingDraftForInteraction(interactionID string) (domain.BotPendingDraft, bool) {
	var row botPendingDraftRow
	if interactionID == "" || r.DB.Where("interaction_id = ? AND status = ?", interactionID, BotDraftPending).Order("created_at DESC").First(&row).Error != nil {
		return domain.BotPendingDraft{}, false
	}
	return row.draft(), true
}

// BeginBotPendingDraft moves a pending, unexpired draft of userID to
// executing and verifies the frozen parameters. Any other state returns the
// current draft with ErrBotDraftNotPending.
func (r *AssistantRepo) BeginBotPendingDraft(id string, userID int64, now time.Time) (domain.BotPendingDraft, error) {
	var out domain.BotPendingDraft
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row botPendingDraftRow
		q := tx
		if r.isMySQL() {
			q = q.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := q.First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		out = row.draft()
		if row.UserID != userID {
			return gorm.ErrRecordNotFound
		}
		if row.Status != BotDraftPending {
			return ErrBotDraftNotPending
		}
		if !now.Before(row.ExpiresAt) {
			out.Status, out.Reason = BotDraftExpired, "草稿已过期"
			if err := tx.Model(&botPendingDraftRow{}).Where("id = ?", id).Updates(map[string]any{"status": BotDraftExpired, "reason": out.Reason, "updated_at": now}).Error; err != nil {
				return err
			}
			return ErrBotDraftNotPending
		}
		if botDraftHash(row.Drafts) != row.ParamsHash {
			out.Status, out.Reason = BotDraftCancelled, ErrBotDraftTampered.Error()
			if err := tx.Model(&botPendingDraftRow{}).Where("id = ?", id).Updates(map[string]any{"status": BotDraftCancelled, "reason": out.Reason, "updated_at": now}).Error; err != nil {
				return err
			}
			return ErrBotDraftNotPending
		}
		result := tx.Model(&botPendingDraftRow{}).Where("id = ? AND status = ?", id, BotDraftPending).Updates(map[string]any{"status": BotDraftExecuting, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrBotDraftNotPending
		}
		out.Status, out.UpdatedAt = BotDraftExecuting, now
		return nil
	})
	return out, err
}

// FinishBotPendingDraft records the outcome of an executing draft.
func (r *AssistantRepo) FinishBotPendingDraft(id, status, result string, now time.Time) error {
	return r.DB.Model(&botPendingDraftRow{}).Where("id = ? AND status = ?", id, BotDraftExecuting).Updates(map[string]any{"status": status, "result": result, "executed_at": now, "updated_at": now}).Error
}

// CancelBotPendingDraft lets the bound user reject a pending draft, or the
// server invalidate it after a failed re-check.
func (r *AssistantRepo) CancelBotPendingDraft(id string, userID int64, reason string, now time.Time) error {
	result := r.DB.Model(&botPendingDraftRow{}).Where("id = ? AND user_id = ? AND status = ?", id, userID, BotDraftPending).Updates(map[string]any{"status": BotDraftCancelled, "reason": truncateBotDetail(reason), "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrBotDraftNotPending
	}
	return nil
}

// cancelBotDraftsWhere invalidates pending drafts (unbinding, Bot disabled,
// deleted or no longer in confirm mode). Executing drafts are left alone.
func cancelBotDraftsWhere(tx *gorm.DB, reason string, query string, args ...any) error {
	return tx.Model(&botPendingDraftRow{}).Where("status = ?", BotDraftPending).Where(query, args...).Updates(map[string]any{"status": BotDraftCancelled, "reason": reason, "updated_at": time.Now().UTC()}).Error
}

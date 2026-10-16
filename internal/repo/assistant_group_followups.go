package repo

import (
	"agentflow/internal/domain"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AssistantGroupFollowUp is a durable, bounded subscription to work explicitly
// authorized by a human turn. It contains no credentials and is never armed by
// opening a conversation or by another automatic follow-up.
type AssistantGroupFollowUp struct {
	RunID              string     `gorm:"primaryKey;size:64" json:"runId"`
	SessionID          string     `gorm:"size:64" json:"-"`
	ProjectID          string     `gorm:"size:128" json:"-"`
	OwnerID            int64      `json:"-"`
	ActorID            int64      `json:"actorId"`
	Model              string     `gorm:"size:256" json:"-"`
	HumanGenerationID  string     `gorm:"size:64" json:"-"`
	Status             string     `gorm:"size:24;index:idx_group_followup_due,priority:1" json:"status"`
	Handled            []byte     `gorm:"type:json" json:"-"`
	Rounds             int        `json:"rounds"`
	ActiveGenerationID string     `gorm:"size:64" json:"generationId,omitempty"`
	LastError          string     `gorm:"type:text" json:"error,omitempty"`
	NextCheckAt        time.Time  `gorm:"index:idx_group_followup_due,priority:2" json:"-"`
	LeaseToken         string     `gorm:"size:64" json:"-"`
	LeaseUntil         *time.Time `json:"-"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

func (AssistantGroupFollowUp) TableName() string { return "assistant_group_followups" }

var ErrAssistantFollowUpStale = errors.New("自动跟进已被更新的用户指令替代")

func (r *AssistantRepo) AssistantGroupFollowUp(runID string) (AssistantGroupFollowUp, bool) {
	var row AssistantGroupFollowUp
	err := r.DB.Where("run_id = ?", runID).Take(&row).Error
	return row, err == nil
}

// BeginAuthorizedGroupGeneration atomically persists the human turn, reserves
// the normal generation slot and arms its completion watcher.
func (r *AssistantRepo) BeginAuthorizedGroupGeneration(g domain.AssistantGeneration, pending domain.AssistantSession, follow AssistantGroupFollowUp) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		if err := scoped.BeginAssistantGeneration(g, pending); err != nil {
			return err
		}
		if !pending.Shared || pending.RunID == "" || follow.RunID != pending.RunID || follow.SessionID != pending.ID || follow.ActorID != g.ActorID || follow.OwnerID != g.UserID || follow.ProjectID != pending.ProjectID || follow.ActorID <= 0 || g.Trigger != "" {
			return fmt.Errorf("群跟进授权无效")
		}
		now := time.Now().UTC()
		follow.HumanGenerationID = g.ID
		follow.Status = "watching"
		follow.Rounds = 0
		follow.ActiveGenerationID = ""
		follow.LastError = ""
		follow.LeaseToken = ""
		follow.LeaseUntil = nil
		follow.NextCheckAt = now
		follow.UpdatedAt = now
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_id"}}, UpdateAll: true}).Create(&follow).Error
	})
}

// ClaimAssistantGroupFollowUps only visits the small, indexed work registry;
// different API replicas may observe the same Runner/Worker completion safely.
func (r *AssistantRepo) ClaimAssistantGroupFollowUps(now time.Time, limit int) ([]AssistantGroupFollowUp, error) {
	now = now.UTC()
	if limit < 1 || limit > 32 {
		limit = 32
	}
	var candidates []AssistantGroupFollowUp
	q := r.DB.Where("status IN ? AND next_check_at <= ? AND (lease_until IS NULL OR lease_until < ?)", []string{"watching", "running"}, now, now).Order("next_check_at ASC").Limit(limit)
	if err := q.Find(&candidates).Error; err != nil {
		return nil, err
	}
	claimed := make([]AssistantGroupFollowUp, 0, len(candidates))
	for _, row := range candidates {
		raw := make([]byte, 20)
		if _, err := rand.Read(raw); err != nil {
			return claimed, err
		}
		token := hex.EncodeToString(raw)
		until := now.Add(30 * time.Second)
		result := r.DB.Model(&AssistantGroupFollowUp{}).Where("run_id = ? AND human_generation_id = ? AND status IN ? AND next_check_at <= ? AND (lease_until IS NULL OR lease_until < ?)", row.RunID, row.HumanGenerationID, []string{"watching", "running"}, now, now).Updates(map[string]any{"lease_token": token, "lease_until": until})
		if result.Error != nil {
			return claimed, result.Error
		}
		if result.RowsAffected == 1 {
			row.LeaseToken = token
			row.LeaseUntil = &until
			claimed = append(claimed, row)
		}
	}
	return claimed, nil
}

func (r *AssistantRepo) DeferAssistantGroupFollowUp(row AssistantGroupFollowUp, status, message string, next time.Time) error {
	next = next.UTC()
	result := r.DB.Model(&AssistantGroupFollowUp{}).Where("run_id = ? AND human_generation_id = ? AND lease_token = ?", row.RunID, row.HumanGenerationID, row.LeaseToken).Updates(map[string]any{"status": status, "last_error": message, "next_check_at": next, "lease_token": "", "lease_until": nil, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssistantFollowUpStale
	}
	return nil
}

// BeginAssistantGroupFollowUpGeneration consumes a completed batch in the same
// transaction as the shared generation slot. A crash cannot replay the batch.
func (r *AssistantRepo) BeginAssistantGroupFollowUpGeneration(row AssistantGroupFollowUp, g domain.AssistantGeneration, expected domain.AssistantSession, handled []string, maxRounds int) error {
	if g.UserID != row.OwnerID || g.ActorID != row.ActorID || g.SessionID != row.SessionID || g.Trigger != "group_followup" || g.ID == "" {
		return fmt.Errorf("自动跟进身份无效")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var session assistantSessionRow
		if err := r.locking(tx).Where("id = ?", row.SessionID).Take(&session).Error; err != nil {
			return err
		}
		saved := decodeAssistantSession(session)
		a, _ := json.Marshal(saved.Messages)
		b, _ := json.Marshal(expected.Messages)
		if !saved.Shared || saved.RunID != row.RunID || saved.UserID != row.OwnerID || !bytes.Equal(a, b) {
			return ErrAssistantFollowUpStale
		}
		var latest AssistantGroupFollowUp
		if err := r.locking(tx).Where("run_id = ? AND human_generation_id = ? AND lease_token = ? AND lease_until > ?", row.RunID, row.HumanGenerationID, row.LeaseToken, time.Now().UTC()).Take(&latest).Error; err != nil {
			return ErrAssistantFollowUpStale
		}
		if latest.Rounds >= maxRounds || latest.Status != "watching" {
			return ErrAssistantFollowUpStale
		}
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		if err := scoped.BeginAssistantGeneration(g); err != nil {
			return err
		}
		raw, err := json.Marshal(handled)
		if err != nil {
			return err
		}
		return tx.Model(&latest).Updates(map[string]any{"handled": raw, "rounds": latest.Rounds + 1, "status": "running", "active_generation_id": g.ID, "lease_token": "", "lease_until": nil, "next_check_at": time.Now().UTC().Add(3 * time.Second), "last_error": "", "updated_at": time.Now().UTC()}).Error
	})
}

func (r *AssistantRepo) FinishAssistantGroupFollowUp(runID, humanID, generationID, status, message string) error {
	result := r.DB.Model(&AssistantGroupFollowUp{}).Where("run_id = ? AND human_generation_id = ? AND active_generation_id = ? AND status = ?", runID, humanID, generationID, "running").Updates(map[string]any{"status": status, "last_error": message, "lease_token": "", "lease_until": nil, "next_check_at": time.Now().UTC().Add(3 * time.Second), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssistantFollowUpStale
	}
	return nil
}

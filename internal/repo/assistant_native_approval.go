package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"time"
)

type assistantNativeApprovalRow struct {
	ID           string `gorm:"primaryKey;size:64"`
	UserID       int64  `gorm:"index:idx_native_approval_session"`
	SessionID    string `gorm:"size:64;index:idx_native_approval_session"`
	GenerationID string `gorm:"size:64"`
	ActorID      int64
	Tool         string `gorm:"size:160"`
	Input        []byte `gorm:"type:longblob"`
	Decision     string `gorm:"size:16"`
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

const nativeApprovalLiveGeneration = `(generation_id = '' OR EXISTS (SELECT 1 FROM assistant_generations g WHERE g.session_id = assistant_native_approvals.session_id AND g.user_id = assistant_native_approvals.user_id AND g.generation_id = assistant_native_approvals.generation_id AND g.status = 'running' AND g.cancel_requested = false))`

func (assistantNativeApprovalRow) TableName() string { return "assistant_native_approvals" }

func (r *AssistantRepo) CreateNativeApproval(owner int64, session string, item domain.AssistantNativeApproval) error {
	return r.DB.Create(&assistantNativeApprovalRow{ID: item.ID, UserID: owner, SessionID: session, GenerationID: item.GenerationID, ActorID: item.ActorID, Tool: item.Tool, Input: item.Input, Decision: "pending", ExpiresAt: item.ExpiresAt.UTC(), CreatedAt: time.Now().UTC()}).Error
}
func nativeApproval(row assistantNativeApprovalRow) domain.AssistantNativeApproval {
	return domain.AssistantNativeApproval{ID: row.ID, GenerationID: row.GenerationID, ActorID: row.ActorID, Tool: row.Tool, Input: row.Input, Decision: row.Decision, ExpiresAt: row.ExpiresAt}
}
func (r *AssistantRepo) NativeApproval(owner int64, session, id string) (domain.AssistantNativeApproval, bool) {
	var row assistantNativeApprovalRow
	err := r.DB.Where("id = ? AND user_id = ? AND session_id = ?", id, owner, session).Take(&row).Error
	return nativeApproval(row), err == nil
}
func (r *AssistantRepo) PendingNativeApprovals(owner int64, session string) []domain.AssistantNativeApproval {
	var rows []assistantNativeApprovalRow
	r.DB.Where("user_id = ? AND session_id = ? AND decision = ? AND expires_at > ?", owner, session, "pending", time.Now().UTC()).Where(nativeApprovalLiveGeneration).Order("created_at").Find(&rows)
	out := make([]domain.AssistantNativeApproval, 0, len(rows))
	for _, row := range rows {
		out = append(out, nativeApproval(row))
	}
	return out
}

// Decisions are scoped to the initiating actor and an exact immutable action.
// Retrying the same answer is harmless; conflicting or expired answers fail.
func (r *AssistantRepo) DecideNativeApproval(owner, actor int64, session, id, decision string) error {
	if decision != "allow" && decision != "deny" {
		return fmt.Errorf("无效审批结果")
	}
	query := r.DB.Model(&assistantNativeApprovalRow{}).Where("id = ? AND user_id = ? AND actor_id = ? AND session_id = ? AND decision = ? AND expires_at > ?", id, owner, actor, session, "pending", time.Now().UTC())
	if decision == "allow" {
		query = query.Where(nativeApprovalLiveGeneration)
	}
	result := query.Update("decision", decision)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		if decision == "allow" {
			var live int64
			r.DB.Model(&assistantNativeApprovalRow{}).Where("id = ? AND user_id = ? AND session_id = ?", id, owner, session).Where(nativeApprovalLiveGeneration).Count(&live)
			if live != 1 {
				return fmt.Errorf("本轮已结束，审批已失效")
			}
		}
		old, ok := r.NativeApproval(owner, session, id)
		if !ok || old.ActorID != actor || old.Decision != decision || !old.ExpiresAt.After(time.Now()) {
			return fmt.Errorf("审批已失效或不属于当前发起人")
		}
	}
	return nil
}

package repo

import (
	"bytes"
	"encoding/json"
	"fmt"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Only the current writer can commit a shared reply. A stale process must not
// overwrite a pending turn after its generation has been cancelled or replaced.
func (r *AssistantRepo) PutAssistantGenerationReply(ownerID int64, generationID string, next domain.AssistantSession) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", next.ID, ownerID).Take(&row).Error; err != nil {
			return err
		}
		var generation assistantGenerationRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ? AND user_id = ?", next.ID, ownerID).Take(&generation).Error; err != nil {
			return err
		}
		if generation.GenerationID != generationID || generation.Status != "running" || generation.CancelRequested {
			return fmt.Errorf("会话生成已取消或被替代，不能保存旧回复")
		}
		old := decodeAssistantSession(row)
		if len(next.Messages) < len(old.Messages) {
			return fmt.Errorf("会话已更新，请重新打开")
		}
		for i, message := range old.Messages {
			a, _ := json.Marshal(message)
			b, _ := json.Marshal(next.Messages[i])
			if !bytes.Equal(a, b) {
				return fmt.Errorf("会话已更新，请重新打开")
			}
		}
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		return scoped.putAssistantSession(ownerID, next)
	})
}

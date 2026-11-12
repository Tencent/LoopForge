package repo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"agentflow/internal/domain"
)

// PutAssistantSessionIfUnchanged saves next only while the stored session is
// still expected and no reply is being generated, so two writers answering
// the same proposal cannot overwrite each other.
func (r *AssistantRepo) PutAssistantSessionIfUnchanged(userID int64, next, expected domain.AssistantSession) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", assistantRecordID(next.ID), userID).Take(&row).Error; err != nil {
			return err
		}
		old := decodeAssistantSession(row)
		a, _ := json.Marshal(old.Messages)
		b, _ := json.Marshal(expected.Messages)
		if !old.UpdatedAt.Equal(expected.UpdatedAt) || !bytes.Equal(a, b) {
			return fmt.Errorf("会话已更新，请重新打开")
		}
		var active int64
		if err := tx.Model(&assistantGenerationRow{}).Where("session_id = ? AND status = ?", row.ID, "running").Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("生成期间不能改写会话")
		}
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		next.UpdatedAt = time.Time{}
		return scoped.putAssistantSession(userID, next)
	})
}

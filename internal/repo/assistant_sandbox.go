package repo

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SetAssistantSessionSandbox uses the same session → generation lock order as
// BeginAssistantGeneration, so a selection cannot race workspace creation.
func (r *AssistantRepo) SetAssistantSessionSandbox(userID int64, id, sandboxID string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", assistantRecordID(id), userID).Take(&row).Error; err != nil {
			return fmt.Errorf("会话不存在")
		}
		s := decodeAssistantSession(row)
		if s.Shared || s.RunID != "" {
			return fmt.Errorf("任务会话使用任务的运行环境，不能切换")
		}
		var generation assistantGenerationRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ?", row.ID).Take(&generation).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && generation.Status == "running" {
			return fmt.Errorf("生成期间不能切换运行环境")
		}
		s.SandboxProfileID = strings.TrimSpace(sandboxID)
		return tx.Model(&assistantSessionRow{}).Where("id = ? AND user_id = ?", row.ID, userID).Update("payload", mustJSON(s)).Error
	})
}

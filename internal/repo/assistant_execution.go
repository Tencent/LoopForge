package repo

import (
	"agentflow/internal/domain"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
)

var ErrAssistantSessionProjectChanged = errors.New("会话项目已变更，请重新打开会话")

var ErrAssistantExecutionLocked = errors.New("会话执行机器已固定，不能更换；请新建对话")

// SetAssistantRuntimeMode changes only the execution mode. The retained
// workspace and execution target survive returning to platform mode.
func (r *AssistantRepo) SetAssistantRuntimeMode(userID int64, id, projectID, mode string, generationID ...string) error {
	if mode != domain.AssistantModePlatform && mode != domain.AssistantModeWorkspace {
		return fmt.Errorf("无效的会话模式")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", assistantRecordID(id), userID).Take(&row).Error; err != nil {
			return err
		}
		item := decodeAssistantSession(row)
		var active assistantGenerationRow
		err := tx.Where("session_id = ? AND status = ?", row.ID, "running").Take(&active).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && (len(generationID) == 0 || generationID[0] == "" || active.GenerationID != generationID[0] || active.CancelRequested) {
			return fmt.Errorf("请先停止当前生成再切换模式")
		}
		if strings.TrimSpace(item.ProjectID) != strings.TrimSpace(projectID) {
			return ErrAssistantSessionProjectChanged
		}
		if mode == domain.AssistantModeWorkspace && item.WorkspaceRunID == "" && item.RunID == "" {
			return fmt.Errorf("请先打开工作区")
		}
		item.RuntimeMode = mode
		return tx.Model(&assistantSessionRow{}).Where("id = ? AND user_id = ?", row.ID, userID).Update("payload", mustJSON(item)).Error
	})
}

package repo

import (
	"agentflow/internal/domain"
	"gorm.io/gorm"
	"time"
)

// SaveAssistantTaskResult updates only runtime fields. A deleted task is never recreated.
func (r *AssistantRepo) SaveAssistantTaskResult(userID int64, result domain.AssistantTask) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantTaskRow
		err := r.locking(tx).Where("id = ? AND user_id = ?", result.ID, userID).First(&row).Error
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		current := decodeAssistantTask(row)
		if current.LastRunAt.UnixMilli() != result.LastRunAt.UnixMilli() {
			return nil
		}
		priorStatus := current.LastStatus
		current.LastStatus = result.LastStatus
		current.LastError = result.LastError
		current.LastMessage = result.LastMessage
		current.NotificationResult = result.NotificationResult
		current.SessionID = result.SessionID
		current.UpdatedAt = time.Now().UTC()
		if err := tx.Model(&row).Updates(map[string]any{"payload": mustJSON(current), "updated_at": timePtr(current.UpdatedAt)}).Error; err != nil {
			return err
		}
		trigger := map[string]string{"ok": "succeeded", "error": "failed"}[result.LastStatus]
		if priorStatus == "running" && trigger != "" {
			content := current.NotificationResult
			if content == "" {
				content = current.LastMessage
			}
			return r.notifications.enqueueTaskNotifications(tx, "assistant_task", current.ID, current.ProjectID, current.Name, trigger, current.LastRunAt, current.NotificationSnapshot, content, current.LastError)
		}
		return nil
	})
}

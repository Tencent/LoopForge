package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"gorm.io/gorm"
	"time"
)

// SaveScheduleJobResult preserves configuration edits and fences deleted/newer task occurrences.
func (r *CatalogRepo) SaveScheduleJobResult(result domain.ScheduleJob) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row scheduleJobRow
		err := r.locking(tx).Where("id = ?", result.ID).First(&row).Error
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		var current domain.ScheduleJob
		if err := json.Unmarshal(row.Payload, &current); err != nil {
			return err
		}
		if current.LastRunAt.UnixMilli() != result.LastRunAt.UnixMilli() {
			return nil
		}
		priorStatus := current.LastStatus
		current.LastStatus = result.LastStatus
		current.LastError = result.LastError
		current.LastPulled = result.LastPulled
		current.LastDispatched = result.LastDispatched
		current.LastAssistantMessage = result.LastAssistantMessage
		current.NotificationResult = result.NotificationResult
		current.Blockers = result.Blockers
		current.UpdatedAt = time.Now().UTC()
		if err := tx.Model(&row).Updates(map[string]any{"payload": mustJSON(current), "updated_at": timePtr(current.UpdatedAt)}).Error; err != nil {
			return err
		}
		trigger := map[string]string{"success": "succeeded", "failed": "failed"}[result.LastStatus]
		if priorStatus == "running" && trigger != "" {
			content := current.NotificationResult
			if content == "" {
				content = current.LastAssistantMessage
			}
			return r.notifications.enqueueTaskNotifications(tx, "schedule_job", current.ID, current.ProjectID, current.Name, trigger, current.LastRunAt, current.NotificationSnapshot, content, current.LastError)
		}
		return nil
	})
}

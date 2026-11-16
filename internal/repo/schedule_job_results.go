package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"gorm.io/gorm"
	"time"
)

// SaveScheduleJobRunSession records the conversation a running execution has
// opened, so an interrupted run still links to it and a fixed job stays
// pinned. Only the occurrence started at lastRunAt changes, and nothing but
// the session fields is written.
func (r *CatalogRepo) SaveScheduleJobRunSession(id string, lastRunAt time.Time, sessionID string) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row scheduleJobRow
		err := r.locking(tx).Where("id = ?", id).First(&row).Error
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
		if current.LastRunAt.UnixMilli() != lastRunAt.UnixMilli() || current.LastStatus != "running" {
			return nil
		}
		current.LastSessionID = sessionID
		if current.UsesFixedSession() {
			current.FixedSessionID = sessionID
		}
		return tx.Model(&row).Updates(map[string]any{"payload": mustJSON(current)}).Error
	})
}

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
		current.LastSessionID = result.LastSessionID
		// A config save meanwhile may have left fixed mode; do not re-pin.
		if result.FixedSessionID != "" && current.UsesFixedSession() {
			current.FixedSessionID = result.FixedSessionID
		}
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

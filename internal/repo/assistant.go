package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func assistantRecordID(id string) string {
	id = filepath.Base(strings.TrimSpace(id))
	if id == "" || id == "." || id == ".." {
		return ""
	}
	return id
}

func (r *AssistantRepo) AssistantSession(userID int64, id string) (domain.AssistantSession, bool) {
	id = assistantRecordID(id)
	if userID <= 0 || id == "" {
		return domain.AssistantSession{}, false
	}
	var row assistantSessionRow
	if err := r.DB.Table("assistant_sessions").Where("id = ? AND user_id = ?", id, userID).Take(&row).Error; err != nil {
		return domain.AssistantSession{}, false
	}
	return decodeAssistantSession(row), true
}

func (r *AssistantRepo) ListAssistantSessions(userID int64) []domain.AssistantSession {
	if userID <= 0 {
		return []domain.AssistantSession{}
	}
	var rows []assistantSessionRow
	if err := r.DB.Table("assistant_sessions").Where("user_id = ?", userID).Order("updated_at DESC").Find(&rows).Error; err != nil {
		return []domain.AssistantSession{}
	}
	out := make([]domain.AssistantSession, 0, len(rows))
	for _, row := range rows {
		item := decodeAssistantSession(row)
		item.Messages = nil
		out = append(out, item)
	}
	return out
}

func (r *AssistantRepo) PutAssistantSession(userID int64, v domain.AssistantSession) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", assistantRecordID(v.ID)).Take(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		return scoped.putAssistantSession(userID, v)
	})
}
func (r *AssistantRepo) putAssistantSession(userID int64, v domain.AssistantSession) error {
	if userID <= 0 {
		return errors.New("assistant session user is required")
	}
	v.ID = assistantRecordID(v.ID)
	if v.ID == "" {
		return errors.New("assistant session id is required")
	}
	if existing, ok := r.sessionByID(v.ID); ok && existing.UserID != userID {
		return errors.New("assistant session belongs to another user")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if old, ok := r.AssistantSession(userID, v.ID); ok && !old.CreatedAt.IsZero() {
		v.CreatedAt = old.CreatedAt
		if !now.After(old.UpdatedAt) {
			now = old.UpdatedAt.Add(time.Millisecond)
		}
		v.HistoryRevision = old.HistoryRevision
		v.ParentSessionID = old.ParentSessionID
		v.RootSessionID = old.RootSessionID
		v.ForkMessageIndex = old.ForkMessageIndex
		if old.ParentSessionID != "" {
			v.Title = old.Title
		}
		v.Archived = old.Archived
		v.Shared = old.Shared
		if old.Shared {
			v.RunID = old.RunID
			v.ProjectID = old.ProjectID
			v.Title = old.Title
		}
	} else if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = now
	}
	v.UserID = userID
	return upsert(r.DB, "assistant_sessions", map[string]any{
		"id":         v.ID,
		"user_id":    userID,
		"project_id": strings.TrimSpace(v.ProjectID),
		"title":      strings.TrimSpace(v.Title),
		"model":      strings.TrimSpace(v.Model),
		"payload":    mustJSON(v),
		"created_at": timePtr(v.CreatedAt),
		"updated_at": timePtr(v.UpdatedAt),
	})
}

func (r *AssistantRepo) SetAssistantSessionArchived(userID int64, id string, archived bool) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", assistantRecordID(id), userID).Take(&row).Error; err != nil {
			return fmt.Errorf("会话不存在")
		}
		item := decodeAssistantSession(row)
		item.Archived = archived
		return tx.Model(&assistantSessionRow{}).Where("id = ? AND user_id = ?", row.ID, userID).Update("payload", mustJSON(item)).Error
	})
}

func (r *AssistantRepo) DeleteAssistantSession(userID int64, id string) error {
	id = assistantRecordID(id)
	if userID <= 0 || id == "" {
		return fmt.Errorf("assistant session not found")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var job assistantGenerationRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ? AND user_id = ?", id, userID).Take(&job).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && job.Status == "running" {
			return fmt.Errorf("请先停止生成再删除会话")
		}
		res := tx.Table("assistant_sessions").Where("id = ? AND user_id = ?", id, userID).Delete(nil)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("assistant session not found")
		}
		return tx.Where("session_id = ? AND user_id = ?", id, userID).Delete(&assistantGenerationRow{}).Error
	})
}

func (r *AssistantRepo) AssistantTask(userID int64, id string) (domain.AssistantTask, bool) {
	id = assistantRecordID(id)
	if userID <= 0 || id == "" {
		return domain.AssistantTask{}, false
	}
	var row assistantTaskRow
	if err := r.DB.Table("assistant_tasks").Where("id = ? AND user_id = ?", id, userID).Take(&row).Error; err != nil {
		return domain.AssistantTask{}, false
	}
	return decodeAssistantTask(row), true
}

func (r *AssistantRepo) ListAssistantTasks(userID int64) []domain.AssistantTask {
	if userID <= 0 {
		return []domain.AssistantTask{}
	}
	var rows []assistantTaskRow
	if err := r.DB.Table("assistant_tasks").Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return []domain.AssistantTask{}
	}
	out := make([]domain.AssistantTask, 0, len(rows))
	for _, row := range rows {
		out = append(out, decodeAssistantTask(row))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (r *AssistantRepo) EnabledAssistantTasks() []domain.AssistantTask {
	var rows []assistantTaskRow
	if err := r.DB.Table("assistant_tasks").Where("enabled = ?", 1).Find(&rows).Error; err != nil {
		return []domain.AssistantTask{}
	}
	out := make([]domain.AssistantTask, 0, len(rows))
	for _, row := range rows {
		out = append(out, decodeAssistantTask(row))
	}
	return out
}

func (r *AssistantRepo) PutAssistantTask(userID int64, v domain.AssistantTask) error {
	if userID <= 0 {
		return errors.New("assistant task user is required")
	}
	v.ID = assistantRecordID(v.ID)
	if v.ID == "" {
		return errors.New("assistant task id is required")
	}
	if existing, ok := r.taskByID(v.ID); ok && existing.UserID != userID {
		return errors.New("assistant task belongs to another user")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantTaskRow
		var prior domain.AssistantTask
		err := r.locking(tx).Where("id = ?", v.ID).First(&row).Error
		if err == nil {
			prior = decodeAssistantTask(row)
			if prior.UserID != userID {
				return errors.New("assistant task belongs to another user")
			}
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		if !(prior.LastStatus == "running" && v.LastStatus != "running") {
			if err := r.notifications.validateNotificationBindings(tx, v.ProjectID, v.Notifications, true); err != nil {
				return err
			}
		}
		now := time.Now().UTC()
		if prior.ID != "" {
			v.CreatedAt = prior.CreatedAt
		} else if v.CreatedAt.IsZero() {
			v.CreatedAt = now
		}
		v.NotificationSnapshot = domain.CloneNotificationBindings(prior.NotificationSnapshot)
		if v.LastStatus == "running" && (prior.LastStatus != "running" || prior.LastRunAt.UnixMilli() != v.LastRunAt.UnixMilli()) {
			v.NotificationSnapshot = domain.CloneNotificationBindings(v.Notifications)
		}
		if v.UpdatedAt.IsZero() {
			v.UpdatedAt = now
		}
		v.UserID = userID
		enabled := 0
		if v.Enabled {
			enabled = 1
		}
		if err := upsert(tx, "assistant_tasks", map[string]any{
			"id":           v.ID,
			"user_id":      userID,
			"project_id":   strings.TrimSpace(v.ProjectID),
			"name":         strings.TrimSpace(v.Name),
			"enabled":      enabled,
			"interval_sec": v.IntervalSec,
			"last_run_at":  timePtr(v.LastRunAt),
			"payload":      mustJSON(v),
			"created_at":   timePtr(v.CreatedAt),
			"updated_at":   timePtr(v.UpdatedAt),
		}); err != nil {
			return err
		}
		trigger := map[string]string{"ok": "succeeded", "error": "failed"}[v.LastStatus]
		if prior.ID != "" && prior.LastStatus == "running" && trigger != "" && !v.LastRunAt.IsZero() {
			result := v.NotificationResult
			if result == "" {
				result = v.LastMessage
			}
			return r.notifications.enqueueTaskNotifications(tx, "assistant_task", v.ID, v.ProjectID, v.Name, trigger, v.LastRunAt, prior.NotificationSnapshot, result, v.LastError)
		}
		return nil
	})
}

func (r *AssistantRepo) DeleteAssistantTask(userID int64, id string) error {
	id = assistantRecordID(id)
	if userID <= 0 || id == "" {
		return fmt.Errorf("assistant task not found")
	}
	res := r.DB.Table("assistant_tasks").Where("id = ? AND user_id = ?", id, userID).Delete(nil)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("assistant task not found")
	}
	return nil
}

func (r *AssistantRepo) sessionByID(id string) (domain.AssistantSession, bool) {
	var row assistantSessionRow
	if err := r.DB.Table("assistant_sessions").Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.AssistantSession{}, false
	}
	return decodeAssistantSession(row), true
}

func (r *AssistantRepo) taskByID(id string) (domain.AssistantTask, bool) {
	var row assistantTaskRow
	if err := r.DB.Table("assistant_tasks").Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.AssistantTask{}, false
	}
	return decodeAssistantTask(row), true
}

func decodeAssistantSession(row assistantSessionRow) domain.AssistantSession {
	var item domain.AssistantSession
	_ = json.Unmarshal(row.Payload, &item)
	item.ID = row.ID
	item.UserID = row.UserID
	if item.ProjectID == "" {
		item.ProjectID = row.ProjectID
	}
	if item.Title == "" {
		item.Title = row.Title
	}
	if item.Model == "" {
		item.Model = row.Model
	}
	if item.CreatedAt.IsZero() && row.Created != nil {
		item.CreatedAt = row.Created.UTC()
	}
	if row.Updated != nil {
		item.UpdatedAt = row.Updated.UTC()
	}
	return item
}

func decodeAssistantTask(row assistantTaskRow) domain.AssistantTask {
	var item domain.AssistantTask
	_ = json.Unmarshal(row.Payload, &item)
	item.ID = row.ID
	item.UserID = row.UserID
	if item.ProjectID == "" {
		item.ProjectID = row.ProjectID
	}
	if item.Name == "" {
		item.Name = row.Name
	}
	if item.IntervalSec == 0 {
		item.IntervalSec = row.IntervalSec
	}
	item.Enabled = row.Enabled == 1
	if item.LastRunAt.IsZero() && row.LastRunAt != nil {
		item.LastRunAt = row.LastRunAt.UTC()
	}
	if item.CreatedAt.IsZero() && row.Created != nil {
		item.CreatedAt = row.Created.UTC()
	}
	if row.Updated != nil {
		item.UpdatedAt = row.Updated.UTC()
	}
	return item
}

func (r *AssistantRepo) AssistantTaskExists(projectID, id string) bool {
	var count int64
	return r.DB.Model(&assistantTaskRow{}).Where("id = ? AND project_id = ?", id, projectID).Count(&count).Error == nil && count > 0
}

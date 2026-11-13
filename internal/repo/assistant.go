package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
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
	rows, err := r.findAssistantSessionSummaries("user_id = ?", userID)
	if err != nil {
		log.Printf("assistant session list failed user=%d: %v", userID, err)
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

// Remove transcripts in SQL so list and permission checks never transfer or
// decode chat history. Retain all other metadata, including legacy fallbacks.
func (r *AssistantRepo) assistantSessionSummaries() *gorm.DB {
	summary := "json(" + r.assistantParticipantSummarySQL() + ")"
	if r.DB.Dialector.Name() != "sqlite" {
		summary = "CAST((" + r.assistantParticipantSummarySQL() + ") AS JSON)"
	}
	return r.DB.Table("assistant_sessions").Select("id, user_id, project_id, title, model, source, task_id, created_at, updated_at, JSON_SET(JSON_REMOVE(payload, '$.messages'), '$.participantSummaryMessages', " + summary + ") AS payload")
}

// noSummaryProjection is process-wide: production has a single datasource.
var noSummaryProjection atomic.Bool

// The legacy participant projection relies on newer JSON functions. If the
// database rejects it, keep the history list available with cached metadata.
// A syntax/function error means the server lacks JSON_TABLE (MySQL 5.7,
// TDSQL-C 5.7): remember it and stop sending the query, so every history list
// does not log the same failure.
// Rows are ordered in Go: MySQL 8 may run out of sort memory when filesorting
// rows that carry large JSON payloads.
func (r *AssistantRepo) findAssistantSessionSummaries(where string, args ...any) ([]assistantSessionRow, error) {
	var rows []assistantSessionRow
	if !noSummaryProjection.Load() {
		err := r.assistantSessionSummaries().Where(where, args...).Find(&rows).Error
		if err == nil {
			sortAssistantSessionRows(rows)
			r.backfillAssistantTitles(rows)
			return rows, nil
		}
		if summaryProjectionUnsupported(err) {
			if noSummaryProjection.CompareAndSwap(false, true) {
				log.Printf("assistant session summary projection unsupported by this database, using metadata fallback from now on: %v", err)
			}
		} else {
			log.Printf("assistant session summary projection failed, using metadata fallback: %v", err)
		}
	}
	var err error
	fallback := func() ([]assistantSessionRow, error) {
		var out []assistantSessionRow
		err := r.DB.Table("assistant_sessions").Select("id, user_id, project_id, title, model, source, task_id, created_at, updated_at, JSON_REMOVE(payload, '$.messages') AS payload").Where(where, args...).Find(&out).Error
		sortAssistantSessionRows(out)
		return out, err
	}
	if rows, err = fallback(); err != nil {
		return rows, err
	}
	if r.backfillAssistantParticipants(rows) > 0 {
		if rows, err = fallback(); err != nil {
			return rows, err
		}
	}
	r.backfillAssistantTitles(rows)
	return rows, nil
}

// AssistantTitleFromMessages names a conversation after its first user message.
func AssistantTitleFromMessages(messages []domain.AssistantMessage) string {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		if title := []rune(strings.TrimSpace(m.Content)); len(title) > 0 {
			if len(title) > 36 {
				return string(title[:36]) + "…"
			}
			return string(title)
		}
	}
	return ""
}

// Project conversation replies once blanked their stored title, so the history
// list showed "新对话". Name those rows after their first user message, once.
// SOP run sessions are always titled by their workflow and are left alone.
func (r *AssistantRepo) backfillAssistantTitles(rows []assistantSessionRow) {
	index := map[string]int{}
	var ids []string
	for i, row := range rows {
		if strings.TrimSpace(row.Title) == "" && !strings.HasPrefix(row.ID, "sop-") {
			index[row.ID] = i
			ids = append(ids, row.ID)
		}
	}
	for start := 0; start < len(ids); start += 50 {
		end := min(start+50, len(ids))
		var full []assistantSessionRow
		if err := r.DB.Table("assistant_sessions").Where("id IN ?", ids[start:end]).Find(&full).Error; err != nil {
			log.Printf("assistant title backfill read failed: %v", err)
			return
		}
		for _, row := range full {
			item := decodeAssistantSession(row)
			title := strings.TrimSpace(item.Title)
			if title == "" {
				title = AssistantTitleFromMessages(item.Messages)
			}
			if title == "" {
				title = "新对话"
			}
			if err := r.DB.Exec("UPDATE assistant_sessions SET title = ? WHERE id = ? AND (title = '' OR title IS NULL)", title, row.ID).Error; err != nil {
				log.Printf("assistant title backfill write failed id=%s: %v", row.ID, err)
				continue
			}
			rows[index[row.ID]].Title = title
		}
	}
}

func sortAssistantSessionRows(rows []assistantSessionRow) {
	at := func(row assistantSessionRow) time.Time {
		if row.Updated == nil {
			return time.Time{}
		}
		return *row.Updated
	}
	sort.SliceStable(rows, func(i, j int) bool { return at(rows[i]).After(at(rows[j])) })
}

// Persist the participant summary for rows written before it was cached, so
// later list requests need neither JSON_TABLE nor transcript transfer.
func (r *AssistantRepo) backfillAssistantParticipants(rows []assistantSessionRow) int {
	var ids []string
	for _, row := range rows {
		var meta struct {
			Version int `json:"participantsVersion"`
		}
		if json.Unmarshal(row.Payload, &meta) == nil && meta.Version != 1 {
			ids = append(ids, row.ID)
		}
	}
	jsonArg := "CAST(? AS JSON)"
	if r.DB.Dialector.Name() == "sqlite" {
		jsonArg = "json(?)"
	}
	updated := 0
	for start := 0; start < len(ids); start += 50 {
		end := min(start+50, len(ids))
		var full []assistantSessionRow
		if err := r.DB.Table("assistant_sessions").Where("id IN ?", ids[start:end]).Find(&full).Error; err != nil {
			log.Printf("assistant participant backfill read failed: %v", err)
			return updated
		}
		for _, row := range full {
			item := decodeAssistantSession(row)
			unknown := "false"
			if item.UnknownParticipants {
				unknown = "true"
			}
			res := r.DB.Exec("UPDATE assistant_sessions SET payload = JSON_SET(payload, '$.participants', "+jsonArg+", '$.participantsVersion', 1, '$.unknownParticipants', "+jsonArg+") WHERE id = ? AND updated_at = ?",
				string(mustJSON(item.Participants)), unknown, row.ID, row.Updated)
			if res.Error != nil {
				log.Printf("assistant participant backfill write failed id=%s: %v", row.ID, res.Error)
				continue
			}
			updated += int(res.RowsAffected)
		}
	}
	return updated
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

// SetAssistantSessionGeneratedTitle replaces a new session's prompt title
// with a model summary. A user rename, a fork, or an earlier summary wins.
func (r *AssistantRepo) SetAssistantSessionGeneratedTitle(userID int64, id, title string) error {
	title = strings.TrimSpace(title)
	id = assistantRecordID(id)
	if userID <= 0 || id == "" {
		return errors.New("assistant session id is required")
	}
	if runes := []rune(title); len(runes) == 0 || len(runes) > 36 {
		return errors.New("assistant title is invalid")
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		if row.UserID != userID {
			return errors.New("assistant session belongs to another user")
		}
		item := decodeAssistantSession(row)
		if item.TitleEdited || item.TitleGenerated || item.ParentSessionID != "" {
			return nil
		}
		item.Title = title
		item.TitleGenerated = true
		item.UpdatedAt = time.Time{}
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		return scoped.putAssistantSession(userID, item)
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
	old, exists := r.AssistantSession(userID, v.ID)
	if exists {
		target := old.Execution()
		if v.ExecutionTarget != nil && *v.ExecutionTarget != target {
			return ErrAssistantExecutionLocked
		}
		v.ExecutionTarget = &target
		v.RuntimeMode = old.Mode()
	} else {
		if v.ExecutionTarget == nil {
			v.ExecutionTarget = &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}
		}
		v.RuntimeMode = domain.AssistantModePlatform
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	if old, ok := r.AssistantSession(userID, v.ID); ok && !old.CreatedAt.IsZero() {
		v.CreatedAt = old.CreatedAt
		if !now.After(old.UpdatedAt) {
			now = old.UpdatedAt.Add(time.Millisecond)
		}
		// Shared project replies are written without a title; keep the
		// one set when the conversation started instead of blanking it.
		if (old.TitleEdited && !v.TitleEdited) || strings.TrimSpace(v.Title) == "" {
			v.Title = old.Title
		}
		// A background summary is not a user rename. Later saves that still
		// carry the original prompt must not put that prompt back.
		if old.TitleGenerated && !v.TitleGenerated && !v.TitleEdited {
			v.Title = old.Title
		}
		v.TitleEdited = old.TitleEdited || v.TitleEdited
		if v.TitleEdited {
			v.TitleGenerated = false
		} else {
			v.TitleGenerated = old.TitleGenerated || v.TitleGenerated
		}
		v.HistoryRevision = old.HistoryRevision
		v.ParentSessionID = old.ParentSessionID
		v.RootSessionID = old.RootSessionID
		v.ForkMessageIndex = old.ForkMessageIndex
		if old.ParentSessionID != "" && !v.TitleEdited {
			v.Title = old.Title
		}
		v.IssueID = old.IssueID
		v.Archived = old.Archived
		v.Shared = old.Shared
		// Environment preferences are changed only by the dedicated, locked setter.
		v.SandboxProfileID = old.SandboxProfileID
		if v.WorkspaceRunID == "" {
			v.WorkspaceRunID = old.WorkspaceRunID
		}
		v.OwnWorkspaceRunIDs = old.OwnWorkspaceRunIDs
		if v.Source == "" {
			v.Source = old.Source
			if v.Source == "" {
				v.Source = inferredAssistantSessionSource(old)
			}
		}
		if v.TaskID == "" {
			v.TaskID = old.TaskID
		}
		if old.Shared {
			v.RunID = old.RunID
			v.ProjectID = old.ProjectID
			// The first generated title may replace a shared SOP label.
			// Every other write keeps the title already stored.
			if !(v.TitleGenerated && !old.TitleGenerated && !old.TitleEdited) {
				v.Title = old.Title
			}
		}
	} else {
		v.OwnWorkspaceRunIDs = nil
		if v.CreatedAt.IsZero() {
			v.CreatedAt = now
		}
	}
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = now
	}
	if v.Source == "" {
		if v.Shared && v.RunID != "" {
			v.Source = domain.AssistantSourceSOP
		} else {
			v.Source = domain.AssistantSourceChat
		}
	}
	v.UserID = userID
	// The latest reply and its read state are projections for the history
	// list, recomputed on every listing; a saved transcript never stores them.
	v.GenerationID, v.Unread = "", false
	deriveAssistantParticipants(&v, v.Messages)
	return upsert(r.DB, "assistant_sessions", map[string]any{
		"id":         v.ID,
		"user_id":    userID,
		"project_id": strings.TrimSpace(v.ProjectID),
		"title":      strings.TrimSpace(v.Title),
		"model":      strings.TrimSpace(v.Model),
		"source":     strings.TrimSpace(v.Source),
		"task_id":    strings.TrimSpace(v.TaskID),
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

// SetAssistantSessionWorkspaceRun binds the workspace Main Bot chose for an
// ordinary conversation, recording it among the conversation's own when the
// conversation opened it. Like archiving it leaves updated_at alone, so a
// generation that is still writing this session does not see a conflict.
func (r *AssistantRepo) SetAssistantSessionWorkspaceRun(userID int64, id, runID string, own bool) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", assistantRecordID(id), userID).Take(&row).Error; err != nil {
			return fmt.Errorf("会话不存在")
		}
		item := decodeAssistantSession(row)
		if item.Shared && item.RunID != "" {
			return fmt.Errorf("任务会话直接使用任务工作区")
		}
		item.WorkspaceRunID = strings.TrimSpace(runID)
		if own && item.WorkspaceRunID != "" && !slices.Contains(item.OwnWorkspaceRunIDs, item.WorkspaceRunID) {
			item.OwnWorkspaceRunIDs = append(item.OwnWorkspaceRunIDs, item.WorkspaceRunID)
		}
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
		if err := tx.Where("session_id = ? AND user_id = ?", id, userID).Delete(&assistantNativeApprovalRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ?", id).Delete(&assistantSessionReadRow{}).Error; err != nil {
			return err
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
	if item.ExecutionTarget == nil {
		item.ExecutionTarget = &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}
	}
	if item.RuntimeMode == "" {
		item.RuntimeMode = domain.AssistantModePlatform
	}
	if item.ProjectID == "" {
		item.ProjectID = row.ProjectID
	}
	if item.Title == "" {
		item.Title = row.Title
	}
	if item.Model == "" {
		item.Model = row.Model
	}
	if item.Source == "" {
		item.Source = row.Source
	}
	if item.TaskID == "" {
		item.TaskID = row.TaskID
	}
	if item.CreatedAt.IsZero() && row.Created != nil {
		item.CreatedAt = row.Created.UTC()
	}
	if row.Updated != nil {
		item.UpdatedAt = row.Updated.UTC()
	}
	if item.Messages != nil {
		deriveAssistantParticipants(&item, item.Messages)
	} else if item.ParticipantsVersion != 1 {
		var summary struct {
			Messages []domain.AssistantMessage `json:"participantSummaryMessages"`
		}
		_ = json.Unmarshal(row.Payload, &summary)
		deriveAssistantParticipants(&item, summary.Messages)
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

package repo

import (
	"agentflow/internal/domain"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"strings"
	"time"
)

func RunAssistantSessionID(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return fmt.Sprintf("sop-%x", sum[:20])
}
func (r *AssistantRepo) SharedAssistantSession(id string) (domain.AssistantSession, bool) {
	var row assistantSessionRow
	if r.DB.Where("id = ?", id).Take(&row).Error != nil {
		return domain.AssistantSession{}, false
	}
	item := decodeAssistantSession(row)
	return item, validSharedAssistantSession(item)
}
func (r *AssistantRepo) EnsureRunAssistantSession(runID, projectID, title string, userID int64) (domain.AssistantSession, error) {
	id := RunAssistantSessionID(runID)
	if item, ok := r.SharedAssistantSession(id); ok {
		return item, nil
	}
	now := time.Now().UTC()
	item := domain.AssistantSession{ID: id, RunID: runID, ProjectID: projectID, Title: title, UserID: userID, Shared: true, Source: domain.AssistantSourceSOP, CreatedAt: now, UpdatedAt: now}
	// Existing task discussions become the initial shared transcript, preserving authors.
	var rows []assistantSessionRow
	if err := r.DB.Where("project_id = ?", projectID).Where(r.sqlJSONText("$.runId")+" = ?", "$.runId", runID).Order("created_at ASC").Find(&rows).Error; err != nil {
		return item, err
	}
	for _, row := range rows {
		old := decodeAssistantSession(row)
		if old.RunID != runID || old.Shared {
			continue
		}
		for _, message := range old.Messages {
			if message.Role == "user" && message.AuthorID == 0 {
				message.AuthorID = old.UserID
				message.AuthorName = fmt.Sprintf("用户 %d", old.UserID)
				if u, ok := r.users.User(old.UserID); ok {
					message.AuthorName = u.LoginName
				}
			}
			item.Messages = append(item.Messages, message)
		}
	}
	deriveAssistantParticipants(&item, item.Messages)
	row := assistantSessionRow{ID: id, UserID: userID, ProjectID: projectID, Title: title, Source: item.Source, TaskID: item.TaskID, Payload: mustJSON(item), Created: &now, Updated: &now}
	if err := r.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return item, err
	}
	saved, ok := r.SharedAssistantSession(id)
	if !ok {
		return item, fmt.Errorf("shared session unavailable")
	}
	return saved, nil
}
func (r *AssistantRepo) ListSharedAssistantSessions() []domain.AssistantSession {
	rows, err := r.findAssistantSessionSummaries("id LIKE ?", "sop-%")
	if err != nil {
		log.Printf("shared assistant session list failed: %v", err)
		return nil
	}
	out := []domain.AssistantSession{}
	for _, row := range rows {
		item := decodeAssistantSession(row)
		if validSharedAssistantSession(item) {
			item.Messages = nil
			out = append(out, item)
		}
	}
	return out
}

// ListCrossUserAssistantSessions returns, in one query, SOP shared sessions
// and project-shared conversations created by other users. The HTTP layer
// filters both by project membership.
func (r *AssistantRepo) ListCrossUserAssistantSessions(excludeUserID int64) (shared, project []domain.AssistantSession) {
	rows, err := r.findAssistantSessionSummaries("id LIKE ? OR (user_id <> ? AND project_id <> ?)", "sop-%", excludeUserID, "")
	if err != nil {
		log.Printf("cross-user assistant session list failed: %v", err)
		return nil, nil
	}
	shared, project = []domain.AssistantSession{}, []domain.AssistantSession{}
	for _, row := range rows {
		item := decodeAssistantSession(row)
		item.Messages = nil
		if strings.HasPrefix(item.ID, "sop-") {
			if validSharedAssistantSession(item) {
				shared = append(shared, item)
			}
		} else if item.UserID != excludeUserID && ProjectSharedAssistantSession(item) {
			project = append(project, item)
		}
	}
	return shared, project
}

func (r *AssistantRepo) PutSharedAssistantReceipt(userID int64, next domain.AssistantSession, expected domain.AssistantSession) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", next.ID, userID).Take(&row).Error; err != nil {
			return err
		}
		old := decodeAssistantSession(row)
		a, _ := json.Marshal(old.Messages)
		b, _ := json.Marshal(expected.Messages)
		if !(old.Shared || ProjectSharedAssistantSession(old)) || !old.UpdatedAt.Equal(expected.UpdatedAt) || !bytes.Equal(a, b) {
			return fmt.Errorf("共享会话已更新，请重新打开")
		}
		var active int64
		if err := tx.Model(&assistantGenerationRow{}).Where("session_id = ? AND status = ?", next.ID, "running").Count(&active).Error; err != nil {
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

func validSharedAssistantSession(s domain.AssistantSession) bool {
	root := RunAssistantSessionID(s.RunID)
	return s.Shared && s.RunID != "" && (s.ID == root || (s.ParentSessionID != "" && s.RootSessionID == root && strings.HasPrefix(s.ID, "sop-branch-")))
}

// ProjectSharedAssistantSession reports whether an ordinary conversation is
// shared with its project's members. SOP run sessions keep their own rules;
// legacy per-user run discussions stay private.
func ProjectSharedAssistantSession(s domain.AssistantSession) bool {
	return !s.Shared && s.RunID == "" && strings.TrimSpace(s.ProjectID) != "" && !strings.HasPrefix(s.ID, "sop-")
}

const assistantSessionMetadataColumns = "id, user_id, project_id, title, model, source, task_id, created_at, updated_at, JSON_REMOVE(payload, '$.messages') AS payload"

// ProjectAssistantSession returns the metadata of a project-shared
// conversation regardless of who created it. Callers must authorize the
// project before exposing or writing the conversation.
func (r *AssistantRepo) ProjectAssistantSession(id string) (domain.AssistantSession, bool) {
	id = assistantRecordID(id)
	if id == "" || strings.HasPrefix(id, "sop-") {
		return domain.AssistantSession{}, false
	}
	var row assistantSessionRow
	if r.DB.Table("assistant_sessions").Select(assistantSessionMetadataColumns).Where("id = ?", id).Take(&row).Error != nil {
		return domain.AssistantSession{}, false
	}
	item := decodeAssistantSession(row)
	return item, ProjectSharedAssistantSession(item)
}

// Freeze the source under its generation lock; a branch never overwrites it.
func (r *AssistantRepo) CreateAssistantSessionBranch(source domain.AssistantSession, branch domain.AssistantSession) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", source.ID, source.UserID).Take(&row).Error; err != nil {
			return err
		}
		old := decodeAssistantSession(row)
		if !old.UpdatedAt.Equal(source.UpdatedAt) {
			return fmt.Errorf("原会话已更新，请重新打开后编辑")
		}
		var active int64
		if err := tx.Model(&assistantGenerationRow{}).Where("session_id = ? AND status = ?", source.ID, "running").Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("请先停止原会话的生成，再创建分支")
		}
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		return scoped.putAssistantSession(branch.UserID, branch)
	})
}

// ReplaceAssistantTurn edits a turn under the same lock as generation startup.
// Shared conversations keep their ID and owner; author attribution is per turn.
func (r *AssistantRepo) ReplaceAssistantTurn(source domain.AssistantSession, index int, edited domain.AssistantMessage, target ...domain.AssistantSession) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", source.ID, source.UserID).Take(&row).Error; err != nil {
			return err
		}
		old := decodeAssistantSession(row)
		if !old.UpdatedAt.Equal(source.UpdatedAt) {
			return fmt.Errorf("会话已更新，请重新打开后编辑")
		}
		if index < 0 || index >= len(old.Messages) || old.Messages[index].Role != "user" {
			return fmt.Errorf("请选择用户消息")
		}
		var active int64
		if err := tx.Model(&assistantGenerationRow{}).Where("session_id = ? AND status = ?", source.ID, "running").Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("请先停止生成再编辑消息")
		}
		prefix := append([]domain.AssistantMessage(nil), old.Messages[:index]...)
		if len(target) > 0 {
			if !source.Shared || source.RunID != target[0].RunID || source.RootSessionID != target[0].ID {
				return fmt.Errorf("无权覆盖主会话")
			}
			var main assistantSessionRow
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", target[0].ID, target[0].UserID).Take(&main).Error; err != nil {
				return err
			}
			old = decodeAssistantSession(main)
			if !old.UpdatedAt.Equal(target[0].UpdatedAt) {
				return fmt.Errorf("主会话已更新，请重新打开")
			}
			if err := tx.Model(&assistantGenerationRow{}).Where("session_id = ? AND status = ?", old.ID, "running").Count(&active).Error; err != nil {
				return err
			}
			if active > 0 {
				return fmt.Errorf("请先停止主会话生成")
			}
		}
		old.Messages = append(prefix, edited)
		old.UpdatedAt = time.Time{}
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		if err := scoped.putAssistantSession(old.UserID, old); err != nil {
			return err
		}
		saved, ok := scoped.AssistantSession(old.UserID, old.ID)
		if !ok {
			return fmt.Errorf("无法读取更新后的会话")
		}
		saved.HistoryRevision++
		return tx.Model(&assistantSessionRow{}).Where("id = ? AND user_id = ?", saved.ID, saved.UserID).Update("payload", mustJSON(saved)).Error
	})
}

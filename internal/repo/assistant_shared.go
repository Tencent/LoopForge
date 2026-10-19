package repo

import (
	"agentflow/internal/domain"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	item := domain.AssistantSession{ID: id, RunID: runID, ProjectID: projectID, Title: title, UserID: userID, Shared: true, CreatedAt: now, UpdatedAt: now}
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
	row := assistantSessionRow{ID: id, UserID: userID, ProjectID: projectID, Title: title, Payload: mustJSON(item), Created: &now, Updated: &now}
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
	var rows []assistantSessionRow
	if r.DB.Where("id LIKE ?", "sop-%").Order("updated_at DESC").Find(&rows).Error != nil {
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

func (r *AssistantRepo) PutSharedAssistantReceipt(userID int64, next domain.AssistantSession, expected domain.AssistantSession) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", next.ID, userID).Take(&row).Error; err != nil {
			return err
		}
		old := decodeAssistantSession(row)
		a, _ := json.Marshal(old.Messages)
		b, _ := json.Marshal(expected.Messages)
		if !old.Shared || !old.UpdatedAt.Equal(expected.UpdatedAt) || !bytes.Equal(a, b) {
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

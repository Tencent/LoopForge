package repo

import (
	"agentflow/internal/domain"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type assistantGenerationRow struct {
	SessionID       string `gorm:"primaryKey;size:64"`
	UserID          int64  `gorm:"index"`
	GenerationID    string `gorm:"size:64"`
	Status          string `gorm:"size:24"`
	CancelRequested bool
	Payload         []byte `gorm:"type:json"`
	UpdatedAt       time.Time
}

func (assistantGenerationRow) TableName() string { return "assistant_generations" }
func decodeGeneration(row assistantGenerationRow) domain.AssistantGeneration {
	var g domain.AssistantGeneration
	_ = json.Unmarshal(row.Payload, &g)
	g.UserID = row.UserID
	g.Status = row.Status
	g.CancelRequested = row.CancelRequested
	g.UpdatedAt = row.UpdatedAt
	return g
}
func (r *AssistantRepo) AssistantGeneration(userID int64, sessionID string) (domain.AssistantGeneration, bool) {
	var row assistantGenerationRow
	if err := r.DB.Where("session_id = ? AND user_id = ?", sessionID, userID).Take(&row).Error; err != nil {
		return domain.AssistantGeneration{}, false
	}
	// A server that died cannot leave a conversation stuck forever. Never replay
	// a model turn automatically, because its tools may already have written.
	if row.Status == "running" && time.Since(row.UpdatedAt) > 45*time.Second {
		g := decodeGeneration(row)
		g.Status = "interrupted"
		g.Error = "服务端生成已中断，请检查已执行操作后重试"
		_ = r.DB.Model(&assistantGenerationRow{}).Where("session_id = ? AND generation_id = ? AND status = ? AND updated_at = ?", sessionID, row.GenerationID, "running", row.UpdatedAt).Updates(map[string]any{"status": g.Status, "payload": mustJSON(g)}).Error
		if err := r.DB.Where("session_id = ? AND user_id = ?", sessionID, userID).Take(&row).Error; err != nil {
			return domain.AssistantGeneration{}, false
		}
	}
	return decodeGeneration(row), true
}
func (r *AssistantRepo) BeginAssistantGeneration(g domain.AssistantGeneration, pending ...domain.AssistantSession) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var session assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", g.SessionID).Take(&session).Error; err == nil && session.UserID != g.UserID {
			return fmt.Errorf("无权访问会话")
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if len(pending) > 0 {
			next := pending[0]
			old := decodeAssistantSession(session)
			if len(next.Messages) < len(old.Messages) {
				return fmt.Errorf("会话已更新，请重新打开后发送")
			}
			for i, m := range old.Messages {
				a, _ := json.Marshal(m)
				b, _ := json.Marshal(next.Messages[i])
				if !bytes.Equal(a, b) {
					return fmt.Errorf("会话已更新，请重新打开后发送")
				}
			}
			scoped := *r
			base := *r.deps
			base.DB = tx
			scoped.deps = &base
			if err := scoped.putAssistantSession(g.UserID, next); err != nil {
				return err
			}
		}

		var old assistantGenerationRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ?", g.SessionID).Take(&old).Error
		if err == nil {
			if old.UserID != g.UserID {
				return fmt.Errorf("无权访问会话")
			}
			if old.Status == "running" {
				return fmt.Errorf("此会话正在生成，请等待完成或停止")
			}
			return tx.Model(&old).Updates(map[string]any{"generation_id": g.ID, "status": "running", "cancel_requested": false, "payload": mustJSON(g), "updated_at": time.Now().UTC()}).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&assistantGenerationRow{SessionID: g.SessionID, UserID: g.UserID, GenerationID: g.ID, Status: "running", Payload: mustJSON(g), UpdatedAt: time.Now().UTC()}).Error
	})
}
func (r *AssistantRepo) CheckpointAssistantGeneration(g domain.AssistantGeneration) error {
	res := r.DB.Model(&assistantGenerationRow{}).Where("session_id = ? AND user_id = ? AND generation_id = ? AND status = ?", g.SessionID, g.UserID, g.ID, "running").Updates(map[string]any{"status": g.Status, "payload": mustJSON(g), "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("生成状态已变化")
	}
	return nil
}
func (r *AssistantRepo) CancelAssistantGeneration(userID int64, sessionID, id string) error {
	res := r.DB.Model(&assistantGenerationRow{}).Where("session_id = ? AND user_id = ? AND generation_id = ? AND status = ?", sessionID, userID, id, "running").Update("cancel_requested", true)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("生成已结束或不存在")
	}
	return nil
}

func (r *AssistantRepo) AssistantGenerationStatuses(ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 {
		return out
	}
	var rows []assistantGenerationRow
	if r.DB.Select("session_id,status,updated_at").Where("session_id IN ?", ids).Find(&rows).Error != nil {
		return out
	}
	for _, row := range rows {
		status := row.Status
		if status == "running" && time.Since(row.UpdatedAt) > 45*time.Second {
			status = "interrupted"
		}
		out[row.SessionID] = status
	}
	return out
}

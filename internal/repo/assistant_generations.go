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
	// FinishedAt is set when a turn ends through a checkpoint and cleared when
	// the next turn starts. Rows that ended before the column existed keep
	// NULL, which is how read tracking leaves older history unmarked.
	FinishedAt *time.Time
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
		g.Failure = &domain.AssistantFailure{Code: "EXECUTION_INTERRUPTED", Phase: "server", Message: g.Error, Suggestion: "检查已执行操作后再重试；可下载诊断信息交给管理员排查。"}
		_ = r.DB.Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&assistantGenerationRow{}).Where("session_id = ? AND generation_id = ? AND status = ? AND updated_at = ?", sessionID, row.GenerationID, "running", row.UpdatedAt).Updates(map[string]any{"status": g.Status, "payload": mustJSON(g)})
			if res.Error != nil || res.RowsAffected != 1 {
				return res.Error
			}
			return checkpointAssistantDiagnostics(tx, g, nil)
		})
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
			if next.IssueID != old.IssueID {
				return fmt.Errorf("关联需求已更新，请重新发送")
			}
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
			if err := tx.Model(&old).Updates(map[string]any{"generation_id": g.ID, "status": "running", "cancel_requested": false, "payload": mustJSON(g), "updated_at": time.Now().UTC(), "finished_at": nil}).Error; err != nil {
				return err
			}
			return checkpointAssistantDiagnostics(tx, g, nil)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&assistantGenerationRow{SessionID: g.SessionID, UserID: g.UserID, GenerationID: g.ID, Status: "running", Payload: mustJSON(g), UpdatedAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		return checkpointAssistantDiagnostics(tx, g, nil)
	})
}
func (r *AssistantRepo) CheckpointAssistantGeneration(g domain.AssistantGeneration, diagnostics ...domain.AssistantDiagnostics) error {
	return r.checkpointAssistantGeneration(g, true, diagnostics)
}

// HeartbeatAssistantGeneration records a running checkpoint whose diagnostic
// evidence has not changed since it was last stored, so the diagnostic payload
// is not rewritten.
func (r *AssistantRepo) HeartbeatAssistantGeneration(g domain.AssistantGeneration) error {
	return r.checkpointAssistantGeneration(g, false, nil)
}

func (r *AssistantRepo) checkpointAssistantGeneration(g domain.AssistantGeneration, withDiagnostics bool, diagnostics []domain.AssistantDiagnostics) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantGenerationRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ? AND user_id = ? AND generation_id = ? AND status = ?", g.SessionID, g.UserID, g.ID, "running").Take(&row).Error
		if err != nil {
			return fmt.Errorf("生成状态已变化")
		}
		var current domain.AssistantGeneration
		_ = json.Unmarshal(row.Payload, &current)
		if g.Steer == nil && current.Steer != nil {
			g.Steer = current.Steer
		}
		now := time.Now().UTC()
		changes := map[string]any{"status": g.Status, "payload": mustJSON(g), "updated_at": now}
		if g.Status != "running" {
			changes["finished_at"] = now
		}
		res := tx.Model(&row).Updates(changes)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("生成状态已变化")
		}
		if !withDiagnostics {
			return nil
		}
		return checkpointAssistantDiagnostics(tx, g, diagnostics)
	})
}

func (r *AssistantRepo) SteerAssistantGeneration(userID int64, sessionID, id string, steer domain.AssistantSteer) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantGenerationRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ? AND user_id = ? AND generation_id = ? AND status = ?", sessionID, userID, id, "running").Take(&row).Error
		if err != nil {
			return fmt.Errorf("生成已结束或不存在")
		}
		g := decodeGeneration(row)
		g.Steer = &steer
		return tx.Model(&row).Updates(map[string]any{"cancel_requested": true, "payload": mustJSON(g), "updated_at": time.Now().UTC()}).Error
	})
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

// AssistantGenerationState is the latest reply of a conversation as the
// history list shows it to one reader.
type AssistantGenerationState struct {
	ID     string
	Status string
	// FinishedAt is when the reply ended. Replies that ended before it was
	// recorded have none, so read tracking leaves older history unmarked.
	FinishedAt *time.Time
	// Seen reports that the reader has opened the conversation since this
	// reply (see MarkAssistantSessionRead).
	Seen bool
}

// AssistantGenerationStatuses reads the latest reply of each conversation
// and whether the reader has seen it, in one query for the whole list.
func (r *AssistantRepo) AssistantGenerationStatuses(readerID int64, ids []string) map[string]AssistantGenerationState {
	out := map[string]AssistantGenerationState{}
	if len(ids) == 0 {
		return out
	}
	var rows []struct {
		SessionID    string
		GenerationID string
		Status       string
		UpdatedAt    time.Time
		FinishedAt   *time.Time
		ReadID       *string
	}
	err := r.DB.Table("assistant_generations AS g").
		Select("g.session_id, g.generation_id, g.status, g.updated_at, g.finished_at, r.generation_id AS read_id").
		Joins("LEFT JOIN assistant_session_reads AS r ON r.session_id = g.session_id AND r.user_id = ?", readerID).
		Where("g.session_id IN ?", ids).
		Scan(&rows).Error
	if err != nil {
		return out
	}
	for _, row := range rows {
		status := row.Status
		if status == "running" && time.Since(row.UpdatedAt) > 45*time.Second {
			status = "interrupted"
		}
		out[row.SessionID] = AssistantGenerationState{
			ID:         row.GenerationID,
			Status:     status,
			FinishedAt: row.FinishedAt,
			Seen:       row.ReadID != nil && *row.ReadID == row.GenerationID,
		}
	}
	return out
}

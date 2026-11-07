package repo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Only the current writer can commit a shared reply. A stale process must not
// overwrite a pending turn after its generation has been cancelled or replaced.
func (r *AssistantRepo) PutAssistantGenerationReply(ownerID int64, generationID string, next domain.AssistantSession) error {
	return r.commitGenerationTranscript(ownerID, generationID, next, false)
}

// CommitAssistantTranscript records a stopped or redirected turn. The owning
// generation may already have been asked to cancel, and still has to keep the
// text written before that request.
func (r *AssistantRepo) CommitAssistantTranscript(ownerID int64, generationID string, next domain.AssistantSession) error {
	return r.commitGenerationTranscript(ownerID, generationID, next, true)
}

func (r *AssistantRepo) commitGenerationTranscript(ownerID int64, generationID string, next domain.AssistantSession, allowStopped bool) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", next.ID, ownerID).Take(&row).Error; err != nil {
			return err
		}
		var generation assistantGenerationRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ? AND user_id = ?", next.ID, ownerID).Take(&generation).Error; err != nil {
			return err
		}
		if generation.GenerationID != generationID || generation.Status != "running" || (!allowStopped && generation.CancelRequested) {
			return fmt.Errorf("会话生成已取消或被替代，不能保存旧回复")
		}
		old := decodeAssistantSession(row)
		if len(next.Messages) < len(old.Messages) {
			return fmt.Errorf("会话已更新，请重新打开")
		}
		for i, message := range old.Messages {
			a, _ := json.Marshal(message)
			b, _ := json.Marshal(next.Messages[i])
			if !bytes.Equal(a, b) {
				return fmt.Errorf("会话已更新，请重新打开")
			}
		}
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		return scoped.putAssistantSession(ownerID, next)
	})
}

// AppendUnfinishedAssistantReply keeps the trace and partial answer of a turn
// that ended without a reply. It writes only while this generation still owns
// the session and the transcript still ends at the question it answered, so a
// retried call, a newer turn or an edited transcript never gets a stray reply.
func (r *AssistantRepo) AppendUnfinishedAssistantReply(ownerID int64, sessionID, generationID string, messageCount int, message domain.AssistantMessage) (bool, error) {
	if messageCount <= 0 || generationID == "" {
		return false, nil
	}
	appended := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var generation assistantGenerationRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ? AND user_id = ?", sessionID, ownerID).Take(&generation).Error; err != nil {
			return err
		}
		if generation.GenerationID != generationID {
			return nil
		}
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", sessionID, ownerID).Take(&row).Error; err != nil {
			return err
		}
		session := decodeAssistantSession(row)
		if len(session.Messages) != messageCount || session.Messages[messageCount-1].Role != "user" {
			return nil
		}
		session.Messages = append(session.Messages, message)
		session.UpdatedAt = time.Time{}
		scoped := *r
		base := *r.deps
		base.DB = tx
		scoped.deps = &base
		if err := scoped.putAssistantSession(ownerID, session); err != nil {
			return err
		}
		appended = true
		return nil
	})
	return appended, err
}

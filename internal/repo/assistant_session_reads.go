package repo

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrAssistantReplyNotFound: the conversation, as its owner holds it, has no
// reply to mark read.
var ErrAssistantReplyNotFound = errors.New("对话不存在或还没有回复")

// assistantSessionReadRow is the latest reply a reader has seen in a
// conversation. The history list marks a reply that completed after it as
// unread until the reader opens the conversation, on any device.
type assistantSessionReadRow struct {
	UserID       int64     `gorm:"column:user_id;primaryKey;autoIncrement:false"`
	SessionID    string    `gorm:"column:session_id;primaryKey;size:64;index:idx_assistant_session_reads_session"`
	GenerationID string    `gorm:"column:generation_id;size:64"`
	Updated      time.Time `gorm:"column:updated_at"`
}

func (assistantSessionReadRow) TableName() string { return "assistant_session_reads" }

// MarkAssistantSessionRead records that the reader has seen the reply of a
// conversation owned by ownerID. Only the latest reply is recorded, so a
// stale tab reporting an older one cannot bring back a mark another tab
// cleared; it reports false and changes nothing.
func (r *AssistantRepo) MarkAssistantSessionRead(readerID, ownerID int64, sessionID, generationID string) (bool, error) {
	sessionID = assistantRecordID(sessionID)
	generationID = strings.TrimSpace(generationID)
	if readerID <= 0 || ownerID <= 0 || sessionID == "" || generationID == "" {
		return false, ErrAssistantReplyNotFound
	}
	var latest assistantGenerationRow
	err := r.DB.Select("generation_id").Where("session_id = ? AND user_id = ?", sessionID, ownerID).Take(&latest).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, ErrAssistantReplyNotFound
	}
	if err != nil {
		return false, err
	}
	if latest.GenerationID != generationID {
		return false, nil
	}
	row := assistantSessionReadRow{UserID: readerID, SessionID: sessionID, GenerationID: generationID, Updated: time.Now().UTC()}
	err = r.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "session_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"generation_id", "updated_at"}),
	}).Create(&row).Error
	return err == nil, err
}

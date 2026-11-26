package repo

import (
	"time"

	"gorm.io/gorm"
)

// botNativeSessionRow maps one Bot model turn to the native CLI session it
// ran in. It is written before the CLI starts, so every Bot-originated native
// log can later be located and cleaned without touching other sources that
// share the user's CLI home. One Bot conversation maps to many rows: one per
// interaction and per native session actually created.
type botNativeSessionRow struct {
	ID                 string    `gorm:"primaryKey;size:64"`
	ConfigID           string    `gorm:"size:191;index"`
	ProjectID          string    `gorm:"size:191"`
	UserID             int64     `gorm:"index"`
	ConversationID     string    `gorm:"size:64"`
	BotSessionID       string    `gorm:"size:64;index"`
	InteractionID      string    `gorm:"size:64;index"`
	Engine             string    `gorm:"size:32"`
	RequestedSessionID string    `gorm:"size:191"`
	NativeSessionID    string    `gorm:"size:191;index"`
	StorageScope       string    `gorm:"size:64"`
	LogPath            string    `gorm:"size:512"`
	Status             string    `gorm:"size:16;index"`
	CleanupStatus      string    `gorm:"size:16"`
	CleanupDetail      string    `gorm:"size:255"`
	CreatedAt          time.Time `gorm:"index"`
	FinishedAt         *time.Time
	CleanedAt          *time.Time
}

func (botNativeSessionRow) TableName() string { return "bot_native_sessions" }

// BotNativeSession is the input for a new mapping.
type BotNativeSession struct {
	ConfigID, ProjectID, ConversationID, BotSessionID, InteractionID string
	UserID                                                           int64
	Engine, RequestedSessionID, StorageScope                         string
}

// StartBotNativeSession records the mapping before the CLI process starts.
// A failure must stop the turn: an unmapped Bot log could not be cleaned.
func (r *AssistantRepo) StartBotNativeSession(in BotNativeSession) (string, error) {
	row := botNativeSessionRow{ID: "bns-" + randomHex(12), ConfigID: in.ConfigID, ProjectID: in.ProjectID, UserID: in.UserID, ConversationID: in.ConversationID, BotSessionID: in.BotSessionID, InteractionID: in.InteractionID, Engine: in.Engine, RequestedSessionID: in.RequestedSessionID, StorageScope: in.StorageScope, Status: "started", CreatedAt: time.Now().UTC()}
	return row.ID, r.DB.Create(&row).Error
}

// FinishBotNativeSession stores the native session ID the CLI reported and the
// terminal state. When the CLI created a different session than the mapping
// already records, a new mapping row is added so both stay traceable.
func (r *AssistantRepo) FinishBotNativeSession(id, nativeSessionID, status string) (string, error) {
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row botNativeSessionRow
		if err := r.locking(tx).First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if row.NativeSessionID != "" && nativeSessionID != "" && row.NativeSessionID != nativeSessionID {
			extra := row
			extra.ID, extra.NativeSessionID, extra.Status, extra.CreatedAt, extra.FinishedAt = "bns-"+randomHex(12), nativeSessionID, status, now, &now
			id = extra.ID
			return tx.Create(&extra).Error
		}
		updates := map[string]any{"status": status, "finished_at": now}
		if nativeSessionID != "" {
			updates["native_session_id"] = nativeSessionID
		}
		return tx.Model(&botNativeSessionRow{}).Where("id = ?", id).Updates(updates).Error
	})
	return id, err
}

// BotNativeSessions lists mappings of one interaction (diagnostics, tests).
func (r *AssistantRepo) BotNativeSessions(interactionID string) ([]BotNativeSession, []string, error) {
	var rows []botNativeSessionRow
	if err := r.DB.Where("interaction_id = ?", interactionID).Order("created_at, id").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	out := make([]BotNativeSession, 0, len(rows))
	native := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, BotNativeSession{ConfigID: row.ConfigID, ProjectID: row.ProjectID, ConversationID: row.ConversationID, BotSessionID: row.BotSessionID, InteractionID: row.InteractionID, UserID: row.UserID, Engine: row.Engine, RequestedSessionID: row.RequestedSessionID, StorageScope: row.StorageScope})
		native = append(native, row.NativeSessionID+"|"+row.Status)
	}
	return out, native, nil
}

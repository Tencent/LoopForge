package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Chat origins of runs and chat clarification sessions (Feishu, DingTalk).

type botChatOriginRow struct {
	ID             string    `gorm:"primaryKey;size:64"`
	ConfigID       string    `gorm:"size:191;index"`
	ProjectID      string    `gorm:"size:191"`
	ConversationID string    `gorm:"size:64;index:idx_bot_chat_origin_conv,priority:1"`
	ChatType       string    `gorm:"size:16"`
	ChatID         string    `gorm:"size:191"`
	SenderID       string    `gorm:"size:191"`
	UserID         int64     `gorm:"not null;default:0"`
	IssueID        string    `gorm:"size:191;index"`
	RunID          string    `gorm:"size:191;index"`
	CreatedAt      time.Time `gorm:"index:idx_bot_chat_origin_conv,priority:2"`
}

func (botChatOriginRow) TableName() string { return "bot_chat_origins" }

func (row botChatOriginRow) origin() domain.BotChatOrigin {
	return domain.BotChatOrigin{ID: row.ID, ConfigID: row.ConfigID, ProjectID: row.ProjectID, ConversationID: row.ConversationID, ChatType: row.ChatType, ChatID: row.ChatID, SenderID: row.SenderID, UserID: row.UserID, IssueID: row.IssueID, RunID: row.RunID, CreatedAt: row.CreatedAt}
}

// RecordBotChatOrigin stores where a run or a requirement came from. The
// first origin of a run wins (a later approval elsewhere does not move its
// notifications); a requirement row is refreshed so the latest one of the
// conversation is found.
func (r *AssistantRepo) RecordBotChatOrigin(o domain.BotChatOrigin) error {
	if o.ConfigID == "" || o.ConversationID == "" || (o.RunID == "" && o.IssueID == "") {
		return nil
	}
	row := botChatOriginRow{ConfigID: o.ConfigID, ProjectID: o.ProjectID, ConversationID: o.ConversationID, ChatType: o.ChatType, ChatID: o.ChatID, SenderID: o.SenderID, UserID: o.UserID, IssueID: o.IssueID, RunID: o.RunID, CreatedAt: time.Now().UTC()}
	if o.RunID != "" {
		row.ID = hexSHA256("run\x00" + o.RunID)
		return r.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	}
	row.ID = BotIssueOriginID(o.ConversationID, o.IssueID)
	return r.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"created_at", "sender_id", "user_id"})}).Create(&row).Error
}

// BotIssueOriginID is the ID of the origin row of a requirement in a
// conversation.
func BotIssueOriginID(conversationID, issueID string) string {
	return hexSHA256("issue\x00" + conversationID + "\x00" + issueID)
}

// BotRunOrigin returns the chat a run was started from.
func (r *AssistantRepo) BotRunOrigin(runID string) (domain.BotChatOrigin, bool) {
	return botRunOrigin(r.DB, runID)
}

func botRunOrigin(db *gorm.DB, runID string) (domain.BotChatOrigin, bool) {
	if runID == "" {
		return domain.BotChatOrigin{}, false
	}
	var row botChatOriginRow
	if err := db.Where("id = ?", hexSHA256("run\x00"+runID)).Limit(1).Find(&row).Error; err != nil || row.ID == "" {
		return domain.BotChatOrigin{}, false
	}
	return row.origin(), true
}

// botStartOrigin is the chat a top-level run names as its start in its
// execution context. It covers the first events of a run started from a
// chat before the run's own origin is recorded; a run started anywhere
// else names none and never goes to a chat its requirement came from.
func botStartOrigin(db *gorm.DB, run domain.Run) (domain.BotChatOrigin, bool) {
	exec := run.ExecutionContext
	if exec.BotOriginID == "" || exec.ParentRunID != "" || exec.IssueID == "" {
		return domain.BotChatOrigin{}, false
	}
	var row botChatOriginRow
	if err := db.Where("id = ? AND issue_id = ?", exec.BotOriginID, exec.IssueID).Limit(1).Find(&row).Error; err != nil || row.ID == "" {
		return domain.BotChatOrigin{}, false
	}
	return row.origin(), true
}

// LatestBotChatIssue is the requirement most recently created or started
// from a conversation.
func (r *AssistantRepo) LatestBotChatIssue(conversationID string) (string, bool) {
	var row botChatOriginRow
	if err := r.DB.Where("conversation_id = ? AND issue_id <> ''", conversationID).Order("created_at desc").Limit(1).Find(&row).Error; err != nil || row.ID == "" {
		return "", false
	}
	return row.IssueID, true
}

type botClarifySessionRow struct {
	ID             string `gorm:"primaryKey;size:64"`
	ConfigID       string `gorm:"size:191;index"`
	ProjectID      string `gorm:"size:191"`
	ConversationID string `gorm:"size:64;index"`
	IssueID        string `gorm:"size:191;index"`
	ChatType       string `gorm:"size:16"`
	ChatID         string `gorm:"size:191"`
	StarterSender  string `gorm:"size:191"`
	StarterUserID  int64
	Status         string `gorm:"size:16;index"`
	Round          int
	// Payload holds the questions, answers, pending confirmations, the
	// conclusion and the round input (JSON).
	Payload        string `gorm:"type:mediumtext"`
	LastAnswerAt   *time.Time
	LastActivityAt time.Time
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (botClarifySessionRow) TableName() string { return "bot_clarify_sessions" }

type botClarifyPayload struct {
	RoundMessage string                      `json:"roundMessage,omitempty"`
	Questions    []domain.BotClarifyQuestion `json:"questions,omitempty"`
	General      []domain.BotClarifyAnswer   `json:"general,omitempty"`
	Pending      []domain.BotClarifyPending  `json:"pending,omitempty"`
	Summary      string                      `json:"summary,omitempty"`
	Error        string                      `json:"error,omitempty"`
}

func (row botClarifySessionRow) session() domain.BotClarifySession {
	s := domain.BotClarifySession{ID: row.ID, ConfigID: row.ConfigID, ProjectID: row.ProjectID, ConversationID: row.ConversationID, ChatType: row.ChatType, ChatID: row.ChatID, IssueID: row.IssueID, StarterSender: row.StarterSender, StarterUserID: row.StarterUserID, Status: row.Status, Round: row.Round, LastAnswerAt: row.LastAnswerAt, LastActivityAt: row.LastActivityAt, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	var p botClarifyPayload
	if json.Unmarshal([]byte(row.Payload), &p) == nil {
		s.RoundMessage, s.Questions, s.General, s.Pending, s.Summary, s.Error = p.RoundMessage, p.Questions, p.General, p.Pending, p.Summary, p.Error
	}
	return s
}

func clarifySessionRow(s domain.BotClarifySession) botClarifySessionRow {
	payload, _ := json.Marshal(botClarifyPayload{RoundMessage: s.RoundMessage, Questions: s.Questions, General: s.General, Pending: s.Pending, Summary: s.Summary, Error: s.Error})
	return botClarifySessionRow{ID: s.ID, ConfigID: s.ConfigID, ProjectID: s.ProjectID, ConversationID: s.ConversationID, IssueID: s.IssueID, ChatType: s.ChatType, ChatID: s.ChatID, StarterSender: s.StarterSender, StarterUserID: s.StarterUserID, Status: s.Status, Round: s.Round, Payload: string(payload), LastAnswerAt: s.LastAnswerAt, LastActivityAt: s.LastActivityAt, Version: s.Version, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

var (
	// ErrBotClarifyActive: the conversation or the requirement already has
	// a clarification in progress (the existing session is returned).
	ErrBotClarifyActive = errors.New("bot_clarify_active")
	// ErrBotClarifyUnchanged lets an update callback leave the row as is.
	ErrBotClarifyUnchanged = errors.New("bot_clarify_unchanged")
)

var botClarifyActiveStatuses = []string{domain.BotClarifyAnalyzing, domain.BotClarifyAsking, domain.BotClarifyPaused, domain.BotClarifyConcluded, domain.BotClarifySaving}

// CreateBotClarifySession opens a session unless the conversation or the
// requirement already has an active one, which is returned with
// ErrBotClarifyActive.
func (r *AssistantRepo) CreateBotClarifySession(s domain.BotClarifySession) (domain.BotClarifySession, error) {
	out := s
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var existing botClarifySessionRow
		if err := r.locking(tx).Where("(conversation_id = ? OR issue_id = ?) AND status IN ?", s.ConversationID, s.IssueID, botClarifyActiveStatuses).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if existing.ID != "" {
			out = existing.session()
			return ErrBotClarifyActive
		}
		now := time.Now().UTC()
		if out.ID == "" {
			out.ID = "clarify-" + randomHex(12)
		}
		out.Version, out.CreatedAt, out.UpdatedAt, out.LastActivityAt = 1, now, now, now
		row := clarifySessionRow(out)
		return tx.Create(&row).Error
	})
	return out, err
}

// BotClarifySession reads one session.
func (r *AssistantRepo) BotClarifySession(id string) (domain.BotClarifySession, error) {
	var row botClarifySessionRow
	if err := r.DB.First(&row, "id = ?", id).Error; err != nil {
		return domain.BotClarifySession{}, err
	}
	return row.session(), nil
}

// ActiveBotClarifySession is the session in progress in a conversation.
func (r *AssistantRepo) ActiveBotClarifySession(conversationID string) (domain.BotClarifySession, bool) {
	var row botClarifySessionRow
	if err := r.DB.Where("conversation_id = ? AND status IN ?", conversationID, botClarifyActiveStatuses).Order("created_at desc").Limit(1).Find(&row).Error; err != nil || row.ID == "" {
		return domain.BotClarifySession{}, false
	}
	return row.session(), true
}

// ActiveBotClarifyIssue reports whether a requirement is being clarified in
// a chat.
func (r *AssistantRepo) ActiveBotClarifyIssue(issueID string) bool {
	var n int64
	r.DB.Model(&botClarifySessionRow{}).Where("issue_id = ? AND status IN ?", issueID, botClarifyActiveStatuses).Count(&n)
	return n > 0
}

// UpdateBotClarifySession applies fn to the current session and writes it
// back if nobody changed it meanwhile (optimistic version check, retried).
// fn may return ErrBotClarifyUnchanged to skip the write; any other error
// is returned as is with the current session.
func (r *AssistantRepo) UpdateBotClarifySession(id string, fn func(*domain.BotClarifySession) error) (domain.BotClarifySession, error) {
	for attempt := 0; attempt < 8; attempt++ {
		current, err := r.BotClarifySession(id)
		if err != nil {
			return current, err
		}
		next := current
		next.Questions = append([]domain.BotClarifyQuestion(nil), current.Questions...)
		for i := range next.Questions {
			next.Questions[i].Answers = append([]domain.BotClarifyAnswer(nil), current.Questions[i].Answers...)
		}
		next.General = append([]domain.BotClarifyAnswer(nil), current.General...)
		next.Pending = append([]domain.BotClarifyPending(nil), current.Pending...)
		if err := fn(&next); err != nil {
			if errors.Is(err, ErrBotClarifyUnchanged) {
				return current, nil
			}
			return current, err
		}
		next.Version = current.Version + 1
		next.UpdatedAt = time.Now().UTC()
		row := clarifySessionRow(next)
		result := r.DB.Model(&botClarifySessionRow{}).Where("id = ? AND version = ?", id, current.Version).Updates(map[string]any{"status": row.Status, "round": row.Round, "payload": row.Payload, "last_answer_at": row.LastAnswerAt, "last_activity_at": row.LastActivityAt, "version": row.Version, "updated_at": row.UpdatedAt})
		if result.Error != nil {
			return current, result.Error
		}
		if result.RowsAffected == 1 {
			return next, nil
		}
	}
	return domain.BotClarifySession{}, errors.New("澄清状态频繁变化，请稍后重试")
}

// OpenBotClarifySessions lists sessions that are analyzing or collecting
// answers, for the timers (next round, pause, interrupted analysis).
func (r *AssistantRepo) OpenBotClarifySessions(limit int) ([]domain.BotClarifySession, error) {
	var rows []botClarifySessionRow
	if err := r.DB.Where("status IN ?", []string{domain.BotClarifyAnalyzing, domain.BotClarifyAsking}).Order("updated_at").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.BotClarifySession, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.session())
	}
	return out, nil
}

// BotInteractionByPlatformMessage finds the message a Bot sent with the
// given platform message ID (used to recognise quotes of clarification
// cards).
func (r *AssistantRepo) BotInteractionByPlatformMessage(botID, platformMessageID string) (domain.BotInteraction, bool) {
	var item domain.BotInteraction
	if platformMessageID == "" {
		return item, false
	}
	if err := r.DB.Where("bot_id = ? AND platform_message_id = ?", botID, platformMessageID).Limit(1).Find(&item).Error; err != nil || item.ID == "" {
		return item, false
	}
	return item, true
}

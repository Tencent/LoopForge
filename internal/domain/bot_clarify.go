package domain

import (
	"strconv"
	"strings"
	"time"
)

// BotChatOrigin records the chat a requirement or run was started from
// (Feishu and DingTalk write confirmations, chat clarification). Events of
// the run are sent back to that conversation, whatever the SOP's own
// notification settings are. One row per started run, plus one per created
// requirement (RunID empty) so "澄清这个需求" can find it.
type BotChatOrigin struct {
	ID             string    `json:"id"`
	ConfigID       string    `json:"configId"`
	ProjectID      string    `json:"projectId"`
	ConversationID string    `json:"conversationId"`
	ChatType       string    `json:"chatType"`
	ChatID         string    `json:"-"`
	SenderID       string    `json:"-"`
	UserID         int64     `json:"-"`
	IssueID        string    `json:"issueId,omitempty"`
	RunID          string    `json:"runId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

// NotificationOriginChat marks deliveries sent to the chat a run was
// started from (NotificationDelivery.Origin).
const NotificationOriginChat = "chat"

// Chat clarification session states.
const (
	BotClarifyAnalyzing = "analyzing" // a round is being analyzed
	BotClarifyAsking    = "asking"    // questions are out, answers are collected
	BotClarifyPaused    = "paused"    // no answer for a while; "继续澄清" resumes
	BotClarifyConcluded = "concluded" // the analysis is ready, waiting to be saved
	BotClarifySaving    = "saving"    // a save is in progress (at most once)
	BotClarifySaved     = "saved"
	BotClarifyEnded     = "ended"
)

// BotClarifyActive reports whether a session still occupies its
// conversation and requirement.
func BotClarifyActive(status string) bool {
	switch status {
	case BotClarifyAnalyzing, BotClarifyAsking, BotClarifyPaused, BotClarifyConcluded, BotClarifySaving:
		return true
	}
	return false
}

// BotClarifySession is one clarification of a requirement run from a chat
// (one per conversation and per requirement at a time). The analysis itself
// is the platform clarification job; the session keeps the numbered
// questions of the current round, the answers collected in the chat (with
// who gave them) and the conclusion waiting to be saved.
type BotClarifySession struct {
	ID             string `json:"id"`
	ConfigID       string `json:"configId"`
	ProjectID      string `json:"projectId"`
	ConversationID string `json:"conversationId"`
	ChatType       string `json:"chatType"`
	ChatID         string `json:"-"`
	IssueID        string `json:"issueId"`
	StarterSender  string `json:"-"`
	StarterUserID  int64  `json:"starterUserId"`
	Status         string `json:"status"`
	Round          int    `json:"round"`
	// RoundMessage is the input of the round being analyzed (kept so an
	// interrupted round can be run again).
	RoundMessage string               `json:"-"`
	Questions    []BotClarifyQuestion `json:"questions,omitempty"`
	// General holds answers that name no question (private chat).
	General []BotClarifyAnswer `json:"general,omitempty"`
	// Pending are messages the Bot asked the speaker about ("这是对 Q2 的
	// 回答吗？").
	Pending []BotClarifyPending `json:"pending,omitempty"`
	Summary string              `json:"summary,omitempty"`
	Error   string              `json:"error,omitempty"`
	// LastAnswerAt is the latest answer of the current round; the next
	// round starts automatically some minutes after it.
	LastAnswerAt   *time.Time `json:"lastAnswerAt,omitempty"`
	LastActivityAt time.Time  `json:"lastActivityAt"`
	Version        int64      `json:"version"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// BotClarifyQuestion is a question of the current round, numbered Q1….
type BotClarifyQuestion struct {
	N        int                `json:"n"`
	ID       string             `json:"id,omitempty"`
	Question string             `json:"question"`
	Options  []string           `json:"options,omitempty"`
	Answers  []BotClarifyAnswer `json:"answers,omitempty"`
}

// BotClarifyAnswer is one chat answer and who gave it.
type BotClarifyAnswer struct {
	Sender string    `json:"sender"`
	Name   string    `json:"name"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// BotClarifyPending is a message the Bot is not sure about; only its
// speaker confirms it as the answer to question N.
type BotClarifyPending struct {
	ID     string    `json:"id"`
	Sender string    `json:"sender"`
	Name   string    `json:"name"`
	Text   string    `json:"text"`
	N      int       `json:"n"`
	At     time.Time `json:"at"`
}

// Question returns question n of the current round.
func (s *BotClarifySession) Question(n int) *BotClarifyQuestion {
	for i := range s.Questions {
		if s.Questions[i].N == n {
			return &s.Questions[i]
		}
	}
	return nil
}

// AnswerCount is the number of answers collected in the current round.
func (s BotClarifySession) AnswerCount() int {
	n := len(s.General)
	for _, q := range s.Questions {
		n += len(q.Answers)
	}
	return n
}

// Unanswered lists the numbers of questions without an answer.
func (s BotClarifySession) Unanswered() []int {
	out := []int{}
	for _, q := range s.Questions {
		if len(q.Answers) == 0 {
			out = append(out, q.N)
		}
	}
	return out
}

// AllAnswered reports whether every question of the round has an answer.
func (s BotClarifySession) AllAnswered() bool {
	return len(s.Questions) > 0 && len(s.Unanswered()) == 0
}

// QuestionLabels formats question numbers as "Q1、Q3".
func QuestionLabels(ns []int) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		parts = append(parts, "Q"+strconv.Itoa(n))
	}
	return strings.Join(parts, "、")
}

// ClarifySaveAllowed reports whether a platform user may save a chat
// clarification into the requirement (and write it back to the external
// tracker): the requirement's responsible person (the assignee, or the
// project owner when nobody is assigned), the member who started the
// clarification, and project maintainers (the owner included).
func ClarifySaveAllowed(project Project, issue Issue, s BotClarifySession, userID int64) bool {
	if userID <= 0 {
		return false
	}
	if userID == s.StarterUserID || userID == project.OwnerID || (issue.AssigneeUserID != 0 && issue.AssigneeUserID == userID) {
		return true
	}
	for _, m := range project.Members {
		if m.UserID == userID && strings.EqualFold(strings.TrimSpace(m.Role), "maintainer") {
			return true
		}
	}
	return false
}

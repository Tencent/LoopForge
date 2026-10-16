package domain

import (
	"strings"
	"time"
)

type AssistantMessage struct {
	Trigger        string                `json:"trigger,omitempty"`
	TriggerID      string                `json:"triggerId,omitempty"`
	StartedAt      *time.Time            `json:"startedAt,omitempty"`
	CreatedAt      time.Time             `json:"createdAt,omitempty"`
	AuthorID       int64                 `json:"authorId,omitempty"`
	AuthorName     string                `json:"authorName,omitempty"`
	SpeakerName    string                `json:"speakerName,omitempty"`
	SpeakerAgentID string                `json:"speakerAgentId,omitempty"`
	Role           string                `json:"role"`
	Content        string                `json:"content"`
	References     []AssistantReference  `json:"references,omitempty"`
	Attachments    []AssistantAttachment `json:"attachments,omitempty"`
	// Process is display-only reasoning and tool activity; it is never sent back to models.
	Process []AssistantProcessStep `json:"process,omitempty"`
}

type AssistantProcessStep struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

const (
	assistantProcessMaxSteps = 60
	assistantProcessMaxChars = 24000
)

// AppendAssistantProcess merges streamed thinking deltas and records tool calls
// within fixed limits.
func AppendAssistantProcess(steps []AssistantProcessStep, kind, text string, full bool) []AssistantProcessStep {
	if text == "" || (kind != "thinking" && kind != "tool") {
		return steps
	}
	used := 0
	for _, step := range steps {
		used += len(step.Text)
	}
	if used >= assistantProcessMaxChars {
		return steps
	}
	if room := assistantProcessMaxChars - used; len(text) > room {
		text = strings.ToValidUTF8(text[:room], "")
	}
	if n := len(steps); n > 0 && kind == "thinking" && steps[n-1].Kind == "thinking" {
		if full {
			if !strings.Contains(steps[n-1].Text, strings.TrimSpace(text)) {
				steps[n-1].Text += "\n" + text
			}
			return steps
		}
		steps[n-1].Text += text
		return steps
	}
	if len(steps) >= assistantProcessMaxSteps {
		return steps
	}
	return append(steps, AssistantProcessStep{Kind: kind, Text: text})
}

// SanitizeAssistantProcess bounds client-supplied history.
func SanitizeAssistantProcess(steps []AssistantProcessStep) []AssistantProcessStep {
	var out []AssistantProcessStep
	used := 0
	for _, step := range steps {
		if len(out) >= assistantProcessMaxSteps || used >= assistantProcessMaxChars {
			break
		}
		if step.Text == "" || (step.Kind != "thinking" && step.Kind != "tool") {
			continue
		}
		if room := assistantProcessMaxChars - used; len(step.Text) > room {
			step.Text = strings.ToValidUTF8(step.Text[:room], "")
		}
		used += len(step.Text)
		out = append(out, step)
	}
	return out
}

// WithoutAssistantProcess returns history suitable for model prompts.
func WithoutAssistantProcess(messages []AssistantMessage) []AssistantMessage {
	out := append([]AssistantMessage(nil), messages...)
	for i := range out {
		out[i].Process = nil
	}
	return out
}

type AssistantReference struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	ProjectID   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
}

type AssistantAttachment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

type AssistantSession struct {
	HistoryRevision  int    `json:"historyRevision,omitempty"`
	ParentSessionID  string `json:"parentSessionId,omitempty"`
	RootSessionID    string `json:"rootSessionId,omitempty"`
	ForkMessageIndex int    `json:"forkMessageIndex,omitempty"`

	Shared           bool               `json:"shared,omitempty"`
	GenerationStatus string             `json:"generationStatus,omitempty"`
	Archived         bool               `json:"archived,omitempty"`
	ID               string             `json:"id"`
	UserID           int64              `json:"userId,omitempty"`
	Title            string             `json:"title"`
	ProjectID        string             `json:"projectId,omitempty"`
	Model            string             `json:"model,omitempty"`
	Executor         string             `json:"executor,omitempty"`
	RunID            string             `json:"runId,omitempty"`
	Messages         []AssistantMessage `json:"messages,omitempty"`
	CreatedAt        time.Time          `json:"createdAt,omitempty"`
	UpdatedAt        time.Time          `json:"updatedAt"`
}

type AssistantTask struct {
	Notifications        []NotificationBinding `json:"notifications,omitempty"`
	NotificationSnapshot []NotificationBinding `json:"notificationSnapshot,omitempty"`
	NotificationResult   string                `json:"notificationResult,omitempty"`
	ID                   string                `json:"id"`
	UserID               int64                 `json:"userId,omitempty"`
	Name                 string                `json:"name"`
	Prompt               string                `json:"prompt"`
	Model                string                `json:"model,omitempty"`
	ProjectID            string                `json:"projectId,omitempty"`
	SessionID            string                `json:"sessionId,omitempty"`
	Cron                 string                `json:"cron,omitempty"`
	IntervalSec          int                   `json:"intervalSec,omitempty"`
	Enabled              bool                  `json:"enabled"`
	LastStatus           string                `json:"lastStatus,omitempty"`
	LastError            string                `json:"lastError,omitempty"`
	LastMessage          string                `json:"lastMessage,omitempty"`
	LastRunAt            time.Time             `json:"lastRunAt,omitempty"`
	CreatedAt            time.Time             `json:"createdAt"`
	UpdatedAt            time.Time             `json:"updatedAt"`
}

package domain

import "time"

// AssistantActivityEvent contains only the user-visible execution stream, not
// raw tool arguments/results. It travels with its reply so later turns cannot
// overwrite the execution history.
type AssistantActivityEvent struct {
	Kind   string     `json:"kind"`
	Text   string     `json:"text,omitempty"`
	Tool   string     `json:"tool,omitempty"`
	Detail string     `json:"detail,omitempty"`
	ID     string     `json:"id,omitempty"`
	Failed bool       `json:"failed,omitempty"`
	Full   bool       `json:"full,omitempty"`
	At     *time.Time `json:"at,omitempty"`
}

type AssistantMessage struct {
	Assets         []AssistantAssetRef      `json:"assets,omitempty"`
	RunIDs         []string                 `json:"runIds,omitempty"`
	Activity       []AssistantActivityEvent `json:"activity,omitempty"`
	Trigger        string                   `json:"trigger,omitempty"`
	TriggerID      string                   `json:"triggerId,omitempty"`
	StartedAt      *time.Time               `json:"startedAt,omitempty"`
	CreatedAt      time.Time                `json:"createdAt,omitempty"`
	AuthorID       int64                    `json:"authorId,omitempty"`
	AuthorName     string                   `json:"authorName,omitempty"`
	SpeakerName    string                   `json:"speakerName,omitempty"`
	SpeakerAgentID string                   `json:"speakerAgentId,omitempty"`
	Model          string                   `json:"model,omitempty"`
	Usage          *TokenUsage              `json:"usage,omitempty"`
	Role           string                   `json:"role"`
	Content        string                   `json:"content"`
	References     []AssistantReference     `json:"references,omitempty"`
	Attachments    []AssistantAttachment    `json:"attachments,omitempty"`
}

type AssistantAssetRef struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	Status    string `json:"status,omitempty"`
	Tool      string `json:"tool,omitempty"`
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

type AssistantParticipant struct {
	ID   int64  `json:"id"`
	Name string `json:"name,omitempty"`
}

type AssistantSession struct {
	IssueID             string                 `json:"issueId,omitempty"`
	Participants        []AssistantParticipant `json:"participants"`
	ParticipantsVersion int                    `json:"participantsVersion,omitempty"`
	UnknownParticipants bool                   `json:"unknownParticipants,omitempty"`

	HistoryRevision  int    `json:"historyRevision,omitempty"`
	ParentSessionID  string `json:"parentSessionId,omitempty"`
	RootSessionID    string `json:"rootSessionId,omitempty"`
	ForkMessageIndex int    `json:"forkMessageIndex,omitempty"`

	Shared           bool   `json:"shared,omitempty"`
	GenerationStatus string `json:"generationStatus,omitempty"`
	Archived         bool   `json:"archived,omitempty"`
	ID               string `json:"id"`
	UserID           int64  `json:"userId,omitempty"`
	Title            string `json:"title"`
	TitleEdited      bool   `json:"titleEdited,omitempty"`
	ProjectID        string `json:"projectId,omitempty"`
	Model            string `json:"model,omitempty"`
	Effort           string `json:"effort,omitempty"`
	Executor         string `json:"executor,omitempty"`
	RunID            string `json:"runId,omitempty"`
	// WorkspaceRunID is the one workspace Main Bot has bound to an ordinary
	// conversation: one the conversation opened, or the SOP run of an issue.
	// Unlike RunID it does not make the session a shared run conversation.
	WorkspaceRunID string `json:"workspaceRunId,omitempty"`
	// OwnWorkspaceRunIDs are the workspaces the conversation opened itself; it
	// may switch back to them and deletes them with the conversation.
	OwnWorkspaceRunIDs []string           `json:"ownWorkspaceRunIds,omitempty"`
	SandboxProfileID   string             `json:"sandboxProfileId,omitempty"`
	Messages           []AssistantMessage `json:"messages,omitempty"`
	CreatedAt          time.Time          `json:"createdAt,omitempty"`
	UpdatedAt          time.Time          `json:"updatedAt"`
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

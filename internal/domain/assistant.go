package domain

import (
	"encoding/json"
	"time"
)

const (
	// AssistantSourceChat is an ordinary conversation.
	AssistantSourceChat = "chat"
	// AssistantSourceSOP is a shared conversation owned by a SOP run.
	AssistantSourceSOP = "sop"
	// AssistantSourceScheduled is a conversation produced by a scheduled task.
	AssistantSourceScheduled = "scheduled"
)

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
	// Parent is the tool call ID of the Agent dispatch that produced this
	// event; it is empty for Main Bot's own steps.
	Parent string `json:"parent,omitempty"`
	// Subagent is the dispatched agent type on an Agent tool event and Role
	// the role it runs as; AgentID is the CLI agent ID reported on its
	// completion, which a later dispatch passes as resume to reuse it.
	Subagent string `json:"subagent,omitempty"`
	Role     string `json:"role,omitempty"`
	AgentID  string `json:"agentId,omitempty"`
	// Input is the task an Agent dispatch hands the subagent and Output the
	// report it returns; the side panel shows them like an SOP node session.
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
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
	// Unfinished names why a reply stopped early (stopped, failed, timed out
	// or interrupted). The reply keeps its partial answer and activity.
	Unfinished string `json:"unfinished,omitempty"`
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
	// Element describes a page element the user picked in the native
	// preview (Type "element"); it is the user's own context, not an object
	// the platform looks up.
	Element *AssistantPageElement `json:"element,omitempty"`
}

// AssistantPageElement is a page element picked in the native preview.
type AssistantPageElement struct {
	URL       string            `json:"url,omitempty"`
	Title     string            `json:"title,omitempty"`
	Selector  string            `json:"selector,omitempty"`
	Tag       string            `json:"tag,omitempty"`
	Text      string            `json:"text,omitempty"`
	HTML      string            `json:"html,omitempty"`
	Rect      *ElementRect      `json:"rect,omitempty"`
	Viewport  *ElementRect      `json:"viewport,omitempty"`
	Styles    map[string]string `json:"styles,omitempty"`
	Component string            `json:"component,omitempty"`
	Framework string            `json:"framework,omitempty"`
	Source    string            `json:"source,omitempty"`
}

// ElementRect is a box in CSS pixels; a viewport only sets its size.
type ElementRect struct {
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
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

const (
	AssistantModePlatform  = "platform"
	AssistantModeWorkspace = "workspace"
)

// AssistantExecutionTarget is selected once, independently of a workspace.
// An empty RunnerID in the cloud pool means the built-in execution host.
type AssistantExecutionTarget struct {
	Pool     string `json:"pool"`
	RunnerID string `json:"runnerId,omitempty"`
}

func (s AssistantSession) Execution() AssistantExecutionTarget {
	if s.ExecutionTarget != nil {
		return *s.ExecutionTarget
	}
	// Conversations created before execution selection all ran in the cloud.
	return AssistantExecutionTarget{Pool: ExecutionModeCloud}
}

func (s AssistantSession) Mode() string {
	if s.RuntimeMode != "" {
		return s.RuntimeMode
	}
	return AssistantModePlatform
}

type AssistantSession struct {
	ExecutionTarget *AssistantExecutionTarget `json:"executionTarget,omitempty"`
	RuntimeMode     string                    `json:"runtimeMode,omitempty"`
	// Source separates ordinary chats, SOP run conversations and scheduled
	// task runs. Legacy rows leave it empty and are inferred when read.
	// TaskID is the scheduled job or assistant task that produced the session,
	// so repeated runs of one task keep a single conversation.
	Source              string                 `json:"source,omitempty"`
	TaskID              string                 `json:"taskId,omitempty"`
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
	GenerationID     string `json:"generationId,omitempty"`
	// Unread marks, for the listing reader, a reply that completed after they
	// last opened the conversation. Listing only; never stored.
	Unread      bool   `json:"unread,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
	ID          string `json:"id"`
	UserID      int64  `json:"userId,omitempty"`
	Title       string `json:"title"`
	TitleEdited bool   `json:"titleEdited,omitempty"`
	// TitleGenerated is set when a background model replaced the first-prompt
	// title. It is not a user rename: later turns keep it, and a real rename clears it.
	TitleGenerated bool   `json:"titleGenerated,omitempty"`
	ProjectID      string `json:"projectId,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Executor       string `json:"executor,omitempty"`
	RunID          string `json:"runId,omitempty"`
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

// AssistantNativeApproval names one immutable native tool invocation.
type AssistantNativeApproval struct {
	ID           string          `json:"id"`
	GenerationID string          `json:"generationId,omitempty"`
	ActorID      int64           `json:"actorId"`
	Tool         string          `json:"tool"`
	Input        json.RawMessage `json:"input"`
	Decision     string          `json:"decision"`
	ExpiresAt    time.Time       `json:"expiresAt"`
}

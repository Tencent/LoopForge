package domain

import "time"

// ChatRoom is a project group chat. Anyone working in the project can read
// and post; membership shown in the room is the project member list.
type ChatRoom struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Name      string    `json:"name"`
	CreatedBy int64     `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ChatMember is a project member shown beside the room.
type ChatMember struct {
	UserID int64  `json:"userId"`
	Name   string `json:"name"`
	Role   string `json:"role,omitempty"`
}

// ChatRoomView is a room plus the people who can see it.
type ChatRoomView struct {
	ChatRoom
	Members []ChatMember `json:"members,omitempty"`
}

// ChatMessage is one group-chat turn. Kind "text" is conversation.
// Kind "dispatch" records an issue created in the room and the SOP it was
// handed to. "progress" is a failure or the run's final result; finished
// steps stay on the dispatch card. "approval" is a step waiting for a person,
// and "decision" records a member approving or sending it back.
// TargetType "role" / "both" and AgentID remain on older messages from when
// a room could assign a role without starting a SOP.
type ChatMessage struct {
	ID            string `json:"id"`
	RoomID        string `json:"roomId"`
	UserID        int64  `json:"userId,omitempty"`
	Author        string `json:"author,omitempty"`
	Kind          string `json:"kind"`
	Body          string `json:"body"`
	IssueID       string `json:"issueId,omitempty"`
	IssueNumber   int    `json:"issueNumber,omitempty"`
	IssueTitle    string `json:"issueTitle,omitempty"`
	TargetType    string `json:"targetType,omitempty"` // sop, role, both
	TargetID      string `json:"targetId,omitempty"`
	TargetName    string `json:"targetName,omitempty"`
	WorkflowID    string `json:"workflowId,omitempty"`
	AgentID       string `json:"agentId,omitempty"`
	TaskID        string `json:"taskId,omitempty"`
	RunID         string `json:"runId,omitempty"`
	DispatchError string `json:"dispatchError,omitempty"`
	WorkflowName  string `json:"workflowName,omitempty"`
	// Lane > 0 marks the extra SOPs of a multi-SOP dispatch; the issue card
	// itself is lane 0.
	Lane int `json:"lane,omitempty"`
	// RunFinal is set on a dispatch message once its run reached a terminal
	// status and the closing progress message was posted.
	RunFinal string `json:"runFinal,omitempty"`

	// Progress / approval messages written by the run watcher.
	NodeID     string `json:"nodeId,omitempty"`
	NodeName   string `json:"nodeName,omitempty"`
	NodeStatus string `json:"nodeStatus,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`

	// Run progress shown on dispatch cards. Filled live while the run is
	// unfinished; RunTotal / RunCompleted are persisted together with RunFinal
	// so settled cards never reload their run.
	RunStatus    string   `json:"runStatus,omitempty"`
	RunTotal     int      `json:"runTotal,omitempty"`
	RunCompleted int      `json:"runCompleted,omitempty"`
	RunCurrent   []string `json:"runCurrent,omitempty"`
	// Resolved is persisted on approval messages once the step moved on.
	Resolved bool `json:"resolved,omitempty"`

	// Requirement-space fields. ThreadID keeps side discussion out of the
	// main timeline; AI* track an assistant reply generated for the space.
	ThreadID        string `json:"threadId,omitempty"`
	ToAI            bool   `json:"toAi,omitempty"`
	AIStatus        string `json:"aiStatus,omitempty"`
	AIError         string `json:"aiError,omitempty"`
	AISessionID     string `json:"aiSessionId,omitempty"`
	AIRequesterID   int64  `json:"aiRequesterId,omitempty"`
	AIProgress      string `json:"aiProgress,omitempty"`
	ArtifactID      string `json:"artifactId,omitempty"`
	ArtifactVersion int    `json:"artifactVersion,omitempty"`
	// Attachments are files the author uploaded with a text message; the
	// blobs stay in the author's assistant attachment store.
	Attachments []AssistantAttachment `json:"attachments,omitempty"`
	// CanReject is view-only: the waiting step has somewhere to send work back.
	CanReject bool `json:"canReject,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
}

// ChatMessage kinds.
const (
	ChatKindText     = "text"
	ChatKindDispatch = "dispatch"
	ChatKindProgress = "progress"
	ChatKindApproval = "approval"
	ChatKindDecision = "decision"
)

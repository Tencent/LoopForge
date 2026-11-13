package domain

import (
	"encoding/json"
	"time"
)

// A generation occupies one private or shared conversation slot, independently of any browser.
type AssistantGeneration struct {
	StartedAt        time.Time                  `json:"startedAt,omitempty"`
	Trigger          string                     `json:"trigger,omitempty"`
	TriggerID        string                     `json:"triggerId,omitempty"`
	ID               string                     `json:"id"`
	SessionID        string                     `json:"sessionId"`
	ActorID          int64                      `json:"actorId,omitempty"`
	ActorName        string                     `json:"actorName,omitempty"`
	UserID           int64                      `json:"-"`
	ProjectID        string                     `json:"projectId"`
	Status           string                     `json:"status"`
	RuntimeMode      string                     `json:"runtimeMode,omitempty"`
	WorkspaceWarning string                     `json:"workspaceWarning,omitempty"`
	Error            string                     `json:"error,omitempty"`
	Events           []AssistantGenerationEvent `json:"events"`
	Stream           *AssistantStreamSnapshot   `json:"stream,omitempty"`
	Reply            json.RawMessage            `json:"reply,omitempty"`
	// Steer is a user message sent while this turn is still running. The
	// turn keeps what it already wrote, then continues from that point.
	Steer           *AssistantSteer `json:"steer,omitempty"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	CancelRequested bool            `json:"cancelRequested,omitempty"`
	// MessageCount is the transcript length this turn answers; an interrupted
	// reply is appended only while the transcript still ends there.
	MessageCount int `json:"messageCount,omitempty"`
}

// AssistantSteer redirects a running reply without discarding it first.
type AssistantSteer struct {
	Content      string                `json:"content"`
	Attachments  []AssistantAttachment `json:"attachments,omitempty"`
	References   []AssistantReference  `json:"references,omitempty"`
	Model        string                `json:"model,omitempty"`
	Effort       string                `json:"effort,omitempty"`
	ApprovalMode string                `json:"approvalMode,omitempty"`
	ActorID      int64                 `json:"actorId,omitempty"`
	ActorName    string                `json:"actorName,omitempty"`
	// Trigger marks the follow-up message; empty means the user steered.
	Trigger string `json:"trigger,omitempty"`
}

// A bounded projection survives eviction of old token events and reconnects.
type AssistantStreamSnapshot struct {
	Sequence     int                      `json:"sequence"`
	Answer       string                   `json:"answer"`
	DraftMessage string                   `json:"draftMessage,omitempty"`
	DraftJSON    string                   `json:"draftJSON,omitempty"`
	Activity     []AssistantActivityEvent `json:"activity,omitempty"`
}
type AssistantGenerationEvent struct {
	Sequence int             `json:"sequence"`
	Data     json.RawMessage `json:"data"`
}

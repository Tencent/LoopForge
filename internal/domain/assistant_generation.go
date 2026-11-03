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
	WorkspaceWarning string                     `json:"workspaceWarning,omitempty"`
	Error            string                     `json:"error,omitempty"`
	Events           []AssistantGenerationEvent `json:"events"`
	Stream           *AssistantStreamSnapshot   `json:"stream,omitempty"`
	Reply            json.RawMessage            `json:"reply,omitempty"`
	UpdatedAt        time.Time                  `json:"updatedAt"`
	CancelRequested  bool                       `json:"-"`
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

package domain

import "time"

// GroupCompletion records acceptance of a delivery independently of node execution
// and verification results. Accepting a result never turns skipped tests into passes.
type GroupCompletion struct {
	ID                    string    `json:"id"`
	ActorID               int64     `json:"actorId"`
	Reason                string    `json:"reason"`
	SourceSessionID       string    `json:"sourceSessionId,omitempty"`
	SourceGenerationID    string    `json:"sourceGenerationId,omitempty"`
	SourceMessageIndex    int       `json:"sourceMessageIndex"`
	SourceHistoryRevision int       `json:"sourceHistoryRevision"`
	CreatedAt             time.Time `json:"createdAt"`
}

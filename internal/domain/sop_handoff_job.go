package domain

import "time"

const (
	SOPHandoffJobPending       = "pending"
	SOPHandoffJobRunning       = "running"
	SOPHandoffJobSucceeded     = "succeeded"
	SOPHandoffJobFailed        = "failed"
	SOPHandoffPhaseSummarizing = "summarizing"
	SOPHandoffPhaseSaving      = "saving"
	SOPHandoffPhaseStarting    = "starting"
	SOPHandoffPhaseDone        = "done"
)

// SOPHandoffJob is the durable latest group-to-SOP operation for a run. ID is
// replaced for each new handoff; immutable handoff artifacts remain archived.
// Owner is a unique claim token, not a stable API instance name.
type SOPHandoffJob struct {
	ID                string     `json:"id"`
	RunID             string     `json:"runId"`
	NodeName          string     `json:"nodeName,omitempty"`
	SummaryPreview    string     `json:"summaryPreview,omitempty"`
	Summary           string     `json:"-"`
	ModelSourceSHA    string     `json:"-"`
	NodeID            string     `json:"nodeId"`
	ActorID           int64      `json:"actorId"`
	Status            string     `json:"status"`
	Phase             string     `json:"phase"`
	Error             string     `json:"error,omitempty"`
	SummaryPath       string     `json:"summaryPath,omitempty"`
	SummaryArtifactID string     `json:"summaryArtifactId,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	Owner             string     `json:"-"`
	LeaseUntil        *time.Time `json:"-"`
	Attempt           int        `json:"attempt"`
}

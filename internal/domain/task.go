package domain

import "time"

type Task struct {
	ID            string            `json:"id"`
	IssueID       string            `json:"issueId"`
	AgentID       string            `json:"agentId"`
	WorkflowID    string            `json:"workflowId"`
	RunID         string            `json:"runId"`
	Status        string            `json:"status"`                 // queued, dispatched, running, completed, failed, cancelled
	DispatchMode  string            `json:"dispatchMode,omitempty"` // primary, scheduled, rerun
	ScheduleJobID string            `json:"scheduleJobId,omitempty"`
	TriggerOrigin string            `json:"triggerOrigin,omitempty"` // manual, timer, manual_run
	Repository    RepositoryContext `json:"repository,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

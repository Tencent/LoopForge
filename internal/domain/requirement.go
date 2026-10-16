package domain

import "time"

type RequirementNodeStage struct {
	NodeID     string `json:"nodeId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs,omitempty"`
	// ChildRunID and Children describe the child run started by an SOP node.
	ChildRunID string                 `json:"childRunId,omitempty"`
	Children   []RequirementNodeStage `json:"children,omitempty"`
}

type RequirementLane struct {
	BindingID           string                 `json:"bindingId,omitempty"`
	Lane                string                 `json:"lane"`
	Label               string                 `json:"label"`
	WorkflowID          string                 `json:"workflowId,omitempty"`
	WorkflowName        string                 `json:"workflowName,omitempty"`
	LatestRunID         string                 `json:"latestRunId,omitempty"`
	LatestTaskID        string                 `json:"latestTaskId,omitempty"`
	Status              string                 `json:"status"`
	Started             bool                   `json:"started"`
	RunCount            int                    `json:"runCount"`
	FailedRuns          int                    `json:"failedRuns"`
	Tokens              int64                  `json:"tokens"`
	CostUSD             float64                `json:"costUsd"`
	ExecutionDurationMs int64                  `json:"executionDurationMs"`
	AssigneeUserID      int64                  `json:"assigneeUserId,omitempty"`
	Order               int                    `json:"order,omitempty"`
	DependsOn           []string               `json:"dependsOn,omitempty"`
	Handoff             []string               `json:"handoff,omitempty"`
	Orphan              bool                   `json:"orphan,omitempty"`
	Stages              []RequirementNodeStage `json:"stages,omitempty"`
}

type RequirementStage struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Hint   string `json:"hint,omitempty"`
}

type RequirementOverall struct {
	RunCount            int       `json:"runCount"`
	FailedRuns          int       `json:"failedRuns"`
	SucceededLanes      int       `json:"succeededLanes"`
	ActiveLanes         int       `json:"activeLanes"`
	Tokens              int64     `json:"tokens"`
	CostUSD             float64   `json:"costUsd"`
	CostKnown           bool      `json:"costKnown"`
	ExecutionDurationMs int64     `json:"executionDurationMs"`
	CycleDurationMs     int64     `json:"cycleDurationMs"`
	FirstRunAt          time.Time `json:"firstRunAt,omitempty"`
	LastRunAt           time.Time `json:"lastRunAt,omitempty"`
	Status              string    `json:"status"`
}

type RequirementSnapshot struct {
	Issue   Issue              `json:"issue"`
	Overall RequirementOverall `json:"overall"`
	Lanes   []RequirementLane  `json:"lanes"`
	Stages  []RequirementStage `json:"stages"`
}

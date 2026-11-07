package domain

import "time"

// InsightSource credentials authorize only ingestion into one project. User
// mappings are configured by project maintainers, never by the reporting client.
type InsightSource struct {
	ID           string           `json:"id"`
	ProjectID    string           `json:"projectId"`
	Name         string           `json:"name"`
	UserMappings map[string]int64 `json:"userMappings"`
	TokenPrefix  string           `json:"tokenPrefix"`
	CreatedAt    time.Time        `json:"createdAt"`
	ExpiresAt    time.Time        `json:"expiresAt"`
	RevokedAt    *time.Time       `json:"revokedAt,omitempty"`
}

type InsightWorkflow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InsightUsage uses inclusive input/output counts; cache and reasoning counts
// are subsets, not additional billable tokens. Unknown cost is represented by null.
type InsightUsage struct {
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	InputTokens      int64    `json:"inputTokens"`
	OutputTokens     int64    `json:"outputTokens"`
	CacheReadTokens  int64    `json:"cacheReadTokens"`
	CacheWriteTokens int64    `json:"cacheWriteTokens"`
	ReasoningTokens  int64    `json:"reasoningTokens"`
	CostUSD          *float64 `json:"costUsd"`
	CostStatus       string   `json:"costStatus"`
}

type InsightExecution struct {
	ID                  string          `json:"id"`
	Type                string          `json:"type"`
	ExternalExecutionID string          `json:"externalExecutionId"`
	Workflow            InsightWorkflow `json:"workflow"`
	ExternalUserID      string          `json:"externalUserId,omitempty"`
	URL                 string          `json:"url,omitempty"`
	StartedAt           time.Time       `json:"startedAt"`
	FinishedAt          time.Time       `json:"finishedAt"`
	// Optional sum of active node durations; wall-clock duration is not a substitute.
	ExecutionDurationMs *int64         `json:"executionDurationMs,omitempty"`
	Status              string         `json:"status"`
	Usage               []InsightUsage `json:"usage"`
}

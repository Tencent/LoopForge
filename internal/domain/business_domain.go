package domain

import "time"

// BusinessDomain is a managed reporting dimension, independent of project execution permissions.
type BusinessDomain struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	ManagerIDs  []int64   `json:"managerIds"`
	ViewerIDs   []int64   `json:"viewerIds"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type BusinessDomainAssignment struct {
	ID           int64     `json:"id"`
	ProjectID    string    `json:"projectId"`
	FromDomainID string    `json:"fromDomainId"`
	ToDomainID   string    `json:"toDomainId"`
	ActorID      int64     `json:"actorId"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"createdAt"`
}

type BusinessDomainProjectUsage struct {
	AnalyticsProjectUsage
	Name             string                  `json:"name"`
	BusinessDomainID string                  `json:"businessDomainId"`
	DeliveryMetrics  BusinessDeliveryMetrics `json:"deliveryMetrics"`
}
type BusinessDomainUsage struct {
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Active          bool                    `json:"active"`
	ProjectCount    int                     `json:"projectCount"`
	Runs            int                     `json:"runs"`
	Success         int                     `json:"success"`
	Failed          int                     `json:"failed"`
	Tokens          int64                   `json:"tokens"`
	CostUSD         float64                 `json:"costUsd"`
	CostCNY         float64                 `json:"costCny"`
	CostKnown       bool                    `json:"costKnown"`
	UniqueIssues    int                     `json:"uniqueIssues"`
	Delivered       int                     `json:"delivered"`
	DeliveryMetrics BusinessDeliveryMetrics `json:"deliveryMetrics"`
}

// BusinessDeliveryMetrics uses completion and merge clocks independently of
// Run creation. Percentiles are computed from individual completed work items.
type BusinessDeliveryMetrics struct {
	CompletionToMergeSamples       int        `json:"completionToMergeSamples"`
	AverageCompletionToMergeMs     int64      `json:"averageCompletionToMergeMs"`
	CompletionToMergeMissingIssues int        `json:"completionToMergeMissingIssues"`
	MergedBeforeCompletionIssues   int        `json:"mergedBeforeCompletionIssues"`
	CompletionToMergeMissingMRs    int        `json:"completionToMergeMissingMrs"`
	MergedBeforeCompletionMRs      int        `json:"mergedBeforeCompletionMrs"`
	CompletedIssues                int        `json:"completedIssues"`
	MergedMRs                      int        `json:"mergedMrs"`
	MergedMRsMissingTime           int        `json:"mergedMrsMissingTime"`
	CompletionTimeMissingIssues    int        `json:"completionTimeMissingIssues"`
	EstimatedCompletionIssues      int        `json:"estimatedCompletionIssues"`
	CycleSamples                   int        `json:"cycleSamples"`
	AverageCycleDurationMs         int64      `json:"averageCycleDurationMs"`
	ExecutionSamples               int        `json:"executionSamples"`
	AverageExecutionDurationMs     int64      `json:"averageExecutionDurationMs"`
	P50CycleDurationMs             int64      `json:"p50CycleDurationMs"`
	P95CycleDurationMs             int64      `json:"p95CycleDurationMs"`
	CompletedTokens                int64      `json:"completedTokens"`
	AverageTokensPerCompletedIssue int64      `json:"averageTokensPerCompletedIssue"`
	LatestCompletedAt              *time.Time `json:"latestCompletedAt,omitempty"`
}

type BusinessDeliveryTrendPoint struct {
	Date                       string `json:"date"`
	CompletedIssues            int    `json:"completedIssues"`
	MergedMRs                  int    `json:"mergedMrs"`
	CycleSamples               int    `json:"cycleSamples"`
	AverageCycleDurationMs     int64  `json:"averageCycleDurationMs"`
	ExecutionSamples           int    `json:"executionSamples"`
	AverageExecutionDurationMs int64  `json:"averageExecutionDurationMs"`
}
type BusinessDomainReport struct {
	Attribution     string                       `json:"attribution"`
	GeneratedAt     time.Time                    `json:"generatedAt"`
	From            time.Time                    `json:"from"`
	To              time.Time                    `json:"to"`
	Summary         AnalyticsSummary             `json:"summary"`
	Delivery        AnalyticsDeliverySummary     `json:"delivery"`
	Trend           []AnalyticsTrendPoint        `json:"trend"`
	Projects        []BusinessDomainProjectUsage `json:"projects"`
	BusinessDomains []BusinessDomainUsage        `json:"businessDomains"`
	DeliveryMetrics BusinessDeliveryMetrics      `json:"deliveryMetrics"`
	DeliveryTrend   []BusinessDeliveryTrendPoint `json:"deliveryTrend"`
	TimeZone        string                       `json:"timeZone"`
	Grain           string                       `json:"grain"`
}

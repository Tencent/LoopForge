package domain

// AnalyticsDashboard is the compact, read-only dashboard. Run quality and
// interventions describe top-level internal SOPs; delivery uses the first
// observed merge to a repository's default branch, independently of run dates.
type AnalyticsDashboard struct {
	Coding      AnalyticsCodingOutcome  `json:"coding"`
	Performance AnalyticsSOPPerformance `json:"performance"`

	SOPRuns                int                          `json:"sopRuns"`
	SucceededRuns          int                          `json:"succeededRuns"`
	FailedRuns             int                          `json:"failedRuns"`
	HumanRuns              int                          `json:"humanRuns"`
	P95ExecutionDurationMs int64                        `json:"p95ExecutionDurationMs"`
	ActiveMembers          int                          `json:"activeMembers"`
	DeliveredIssues        int                          `json:"deliveredIssues"`
	SOPDeliveredIssues     int                          `json:"sopDeliveredIssues"`
	VibeDeliveredIssues    int                          `json:"vibeDeliveredIssues"`
	Trend                  []AnalyticsDashboardPoint    `json:"trend"`
	HumanTrend             []AnalyticsInterventionPoint `json:"humanTrend"`
	HumanNodes             []AnalyticsInterventionNode  `json:"humanNodes"`
}

type AnalyticsDashboardPoint struct {
	Coding      AnalyticsCodingOutcome  `json:"coding"`
	Performance AnalyticsSOPPerformance `json:"performance"`

	Date          string `json:"date"`
	SOPRuns       int    `json:"sopRuns"`
	SucceededRuns int    `json:"succeededRuns"`
	FailedRuns    int    `json:"failedRuns"`
	HumanRuns     int    `json:"humanRuns"`
	SOPDelivered  int    `json:"sopDelivered"`
	VibeDelivered int    `json:"vibeDelivered"`
}

// Coding outcomes describe the current PR/delivery state of distinct issues
// linked to coding workspaces created in this cohort, not all created issues.
type AnalyticsCodingOutcome struct {
	Issues    int `json:"issues"`
	WithMR    int `json:"withMr"`
	Delivered int `json:"delivered"`
}

type AnalyticsSOPPerformance struct {
	MeasuredRuns               int   `json:"measuredRuns"`
	AverageExecutionDurationMs int64 `json:"averageExecutionDurationMs"`
	P95ExecutionDurationMs     int64 `json:"p95ExecutionDurationMs"`
	ToolCallsSucceeded         int   `json:"toolCallsSucceeded"`
	ToolCallsFailed            int   `json:"toolCallsFailed"`
}

type AnalyticsHumanCounts struct {
	Approvals  int `json:"approvals"`
	Rejections int `json:"rejections"`
	Retries    int `json:"retries"`
}

type AnalyticsInterventionPoint struct {
	Date string `json:"date"`
	Node string `json:"node"`
	AnalyticsHumanCounts
}

type AnalyticsInterventionNode struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

package domain

// AnalyticsDashboard is the compact, read-only dashboard. Run quality and
// interventions describe top-level internal SOPs; delivery uses the first
// observed merge to a repository's default branch, independently of run dates.
type AnalyticsDashboard struct {
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
	Date          string `json:"date"`
	SOPRuns       int    `json:"sopRuns"`
	SOPDelivered  int    `json:"sopDelivered"`
	VibeDelivered int    `json:"vibeDelivered"`
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

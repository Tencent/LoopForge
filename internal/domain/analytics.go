package domain

import (
	"slices"
	"time"
)

// AnalyticsFilter scopes platform analytics without coupling the store to HTTP.
type AnalyticsFilter struct {
	Origin string // empty/all, internal, external
	// ProjectIDs is a server-resolved scope; nil means unrestricted, empty means no projects.
	ProjectIDs []string `json:"-"`
	ProjectID  string
	WorkflowID string
	From       time.Time
	To         time.Time
	// Scope "overview" skips delivery scans and detailed breakdowns for faster dashboards.
	Scope string
	// UserID keeps runs whose responsible executor or linked issue assignee matches.
	UserID string
	// IssueSource keeps only runs/deliveries whose issue comes from a source: local, gongfeng, tapd.
	IssueSource string
	// RequirementsOnly includes platform and TAPD requirements, excluding issue-fix runs.
	RequirementsOnly bool
	// Grain buckets Trend as day (default), week, month, or all (month buckets, no window).
	Grain string
	// TimeZone is the IANA zone used by business-domain delivery trend buckets
	// and by the analytics weekday × hour heatmap (default Asia/Shanghai).
	TimeZone string
}

type AnalyticsSummary struct {
	ExternalRuns      int       `json:"externalRuns"`
	QualityRuns       int       `json:"qualityRuns"`
	TotalRuns         int       `json:"totalRuns"`
	ActiveRuns        int       `json:"activeRuns"`
	WaitingRuns       int       `json:"waitingRuns"`
	SucceededRuns     int       `json:"succeededRuns"`
	FailedRuns        int       `json:"failedRuns"`
	CancelledRuns     int       `json:"cancelledRuns"`
	ReworkedRuns      int       `json:"reworkedRuns"`
	TotalReworks      int       `json:"totalReworks"`
	CompletionRate    float64   `json:"completionRate"`
	FirstPassRate     float64   `json:"firstPassRate"`
	AverageDurationMs int64     `json:"averageDurationMs"`
	P50DurationMs     int64     `json:"p50DurationMs"`
	P95DurationMs     int64     `json:"p95DurationMs"`
	P99DurationMs     int64     `json:"p99DurationMs"`
	TotalTokens       int64     `json:"totalTokens"`
	TotalCostUSD      float64   `json:"totalCostUsd"`
	TotalCostCNY      float64   `json:"totalCostCny"`
	ToolCalls         int       `json:"toolCalls"`
	Artifacts         int       `json:"artifacts"`
	TelemetryEvents   int       `json:"telemetryEvents"`
	LogRecords        int       `json:"logRecords"`
	GeneratedAt       time.Time `json:"generatedAt"`

	// Conversation* cover conversations that changed code directly in a
	// conversation workspace, without an SOP. Their tokens are part of
	// TotalTokens/TotalCost*, but the workspaces are not SOP runs and stay out
	// of TotalRuns and every run status or duration metric.
	ConversationWorkspaces int `json:"conversationWorkspaces"`
	// ConversationLinkedWorkspaces have a requirement linked.
	ConversationLinkedWorkspaces int `json:"conversationLinkedWorkspaces"`
	ConversationIssues           int `json:"conversationIssues"`
	// Of ConversationIssues, those with a submitted MR and those whose MR
	// merged to the default branch. Unlike Delivery, also set in overview scope.
	ConversationIssuesWithMR    int     `json:"conversationIssuesWithMr"`
	ConversationIssuesDelivered int     `json:"conversationIssuesDelivered"`
	ConversationTokens          int64   `json:"conversationTokens"`
	ConversationCostUSD         float64 `json:"conversationCostUsd"`
	ConversationCostCNY         float64 `json:"conversationCostCny"`

	CostCurrency       string  `json:"costCurrency"`
	USDToCNY           float64 `json:"usdToCny"`
	CostPricingVersion string  `json:"costPricingVersion"`

	// Token 四分项：输入/输出/缓存读/缓存写，合计不等于 TotalTokens（TotalTokens 为账单口径）。
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens,omitempty"`
	// CacheHitRate = ΣCacheRead ÷ Σ(Input + CacheRead) × 100，同源口径内的缓存命中率。
	CacheHitRate float64 `json:"cacheHitRate"`

	// SuccessRate 排除 cancelled（success ÷ (success + failed)）；CompletionRate 保留原闭环率（cancelled 在分母内）。
	SuccessRate float64 `json:"successRate"`
	// CancelRate = cancelled ÷ 全部终态执行 × 100。
	CancelRate float64 `json:"cancelRate"`

	// 执行耗时 = Σ(SOP 节点 FinishedAt−StartedAt)，不含审批/排队/恢复等待；
	// AverageDurationMs 系列仍为端到端墙钟（Run 创建 → 最后结束），作为参考列保留。
	ExecutionAverageDurationMs int64 `json:"executionAverageDurationMs"`
	ExecutionP50DurationMs     int64 `json:"executionP50DurationMs"`
	ExecutionP95DurationMs     int64 `json:"executionP95DurationMs"`
	ExecutionP99DurationMs     int64 `json:"executionP99DurationMs"`
	// WaitAverageDurationMs = 平均端到端墙钟 − 平均执行耗时，回答"卡在等什么"。
	WaitAverageDurationMs int64 `json:"waitAverageDurationMs"`

	// 成本识别：costStatus=reported/estimated 视为已识别，unknown 展示为"未识别"而非 ¥0。
	CostCoveragePct float64 `json:"costCoveragePct"`
	CostReportedUSD float64 `json:"costReportedUsd"`
	CostReportedCNY float64 `json:"costReportedCny"`
	CostUnknownRuns int     `json:"costUnknownRuns"`

	// 需求维度：UniqueIssues 为关联到执行的需求数（去重），后两项为单需求平均执行/失败次数。
	UniqueIssues     int     `json:"uniqueIssues"`
	RunsPerIssue     float64 `json:"runsPerIssue"`
	FailuresPerIssue float64 `json:"failuresPerIssue"`
	// InProgressIssues 为窗口结束时仍未交付的需求数（WIP 快照）。
	InProgressIssues int `json:"inProgressIssues"`
	// 需求耗时分布：按需求聚合的节点求和耗时。
	IssueP50ExecutionDurationMs int64 `json:"issueP50ExecutionDurationMs"`
	IssueP95ExecutionDurationMs int64 `json:"issueP95ExecutionDurationMs"`

	// 人均周吞吐：近 7 天唯一交付需求数 ÷ 近 7 天有交付成员数。
	WeeklyDeliveredIssues    int     `json:"weeklyDeliveredIssues"`
	WeeklyActiveContributors int     `json:"weeklyActiveContributors"`
	WeeklyThroughputPerUser  float64 `json:"weeklyThroughputPerUser"`

	// MergedMRs counts unique linked work MRs by their actual merge time.
	MergedMRs int `json:"mergedMrs"`
	// MergedMRsMissingTime cannot be assigned to the requested date window.
	MergedMRsMissingTime int `json:"mergedMrsMissingTime"`
}

type AnalyticsTrendPoint struct {
	CostUnknownRuns int     `json:"costUnknownRuns"`
	Date            string  `json:"date"`
	Runs            int     `json:"runs"`
	Succeeded       int     `json:"succeeded"`
	Failed          int     `json:"failed"`
	Reworks         int     `json:"reworks"`
	DurationMs      int64   `json:"durationMs"`
	Tokens          int64   `json:"tokens"`
	CostUSD         float64 `json:"costUsd"`
	CostCNY         float64 `json:"costCny"`

	// 执行耗时（节点求和）与 Token 四分项，随趋势一并输出。
	ExecutionDurationMs int64 `json:"executionDurationMs"`
	InputTokens         int64 `json:"inputTokens"`
	OutputTokens        int64 `json:"outputTokens"`
	CacheReadTokens     int64 `json:"cacheReadTokens"`
	CacheWriteTokens    int64 `json:"cacheWriteTokens"`

	// Issues 为当日有执行的去重需求数，供周/月需求量趋势使用。
	Issues    int `json:"issues,omitempty"`
	MergedMRs int `json:"mergedMrs"`

	// Conversation* is the share of Tokens/Cost* used by conversation
	// workspaces in this bucket; they are not SOP runs, and their
	// requirements are not part of Issues.
	ConversationWorkspaces int     `json:"conversationWorkspaces,omitempty"`
	ConversationIssues     int     `json:"conversationIssues,omitempty"`
	ConversationTokens     int64   `json:"conversationTokens,omitempty"`
	ConversationCostUSD    float64 `json:"conversationCostUsd,omitempty"`
	ConversationCostCNY    float64 `json:"conversationCostCny,omitempty"`
}

// AnalyticsWeekHourCell is one non-empty weekday × hour bucket in the requested time zone.
type AnalyticsWeekHourCell struct {
	// Weekday counts from Monday=0 to Sunday=6.
	Weekday int     `json:"weekday"`
	Hour    int     `json:"hour"`
	Runs    int     `json:"runs"`
	Tokens  int64   `json:"tokens"`
	CostUSD float64 `json:"costUsd"`
	CostCNY float64 `json:"costCny"`
}

// AnalyticsMRMerge retains the timestamp evidence used for the MR count.
type AnalyticsMRMerge struct {
	MRProject    string    `json:"mrProject"`
	MRIID        int       `json:"mrIid"`
	MRURL        string    `json:"mrUrl"`
	TargetBranch string    `json:"targetBranch"`
	IssueID      string    `json:"issueId"`
	Title        string    `json:"title"`
	MergedAt     time.Time `json:"mergedAt"`
}

type AnalyticsBreakdown struct {
	CostUnknownRuns    int     `json:"costUnknownRuns"`
	QualityUnknownRuns int     `json:"qualityUnknownRuns"`
	Key                string  `json:"key"`
	Name               string  `json:"name"`
	Runs               int     `json:"runs"`
	Succeeded          int     `json:"succeeded"`
	Failed             int     `json:"failed"`
	Active             int     `json:"active"`
	Reworks            int     `json:"reworks"`
	SuccessRate        float64 `json:"successRate"`
	AverageDurationMs  int64   `json:"averageDurationMs"`
	P95DurationMs      int64   `json:"p95DurationMs"`
	Tokens             int64   `json:"tokens"`
	CostUSD            float64 `json:"costUsd"`
	CostCNY            float64 `json:"costCny"`

	Cancelled int `json:"cancelled"`
	// CancelRate/ReworkRate 单位 %：cancelled ÷ 终态、reworks ÷ runs。
	CancelRate float64 `json:"cancelRate"`
	ReworkRate float64 `json:"reworkRate"`

	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`

	// AverageExecutionDurationMs/P95ExecutionDurationMs 为节点求和口径。
	AverageExecutionDurationMs int64 `json:"averageExecutionDurationMs"`
	P95ExecutionDurationMs     int64 `json:"p95ExecutionDurationMs"`

	// TokenSharePct/CostSharePct 为该维度占全局总量的比例（%）。
	TokenSharePct float64 `json:"tokenSharePct"`
	CostSharePct  float64 `json:"costSharePct"`

	ToolCalls int `json:"toolCalls"`

	// CompletedIssues 为按需求去重后的成功交付数（分类、需求来源等维度使用）。
	CompletedIssues int `json:"completedIssues,omitempty"`

	// 节点维度：重试/失败/返工节点数与比率。
	NodeRuns     int     `json:"nodeRuns,omitempty"`
	RetriedNodes int     `json:"retriedNodes,omitempty"`
	RetryRate    float64 `json:"retryRate,omitempty"`
	FailedNodes  int     `json:"failedNodes,omitempty"`
	ReworkNodes  int     `json:"reworkNodes,omitempty"`

	// 模型与编排画像。
	Model     string            `json:"model,omitempty"`
	ModelMix  map[string]int    `json:"modelMix,omitempty"`
	NodeCount int               `json:"nodeCount,omitempty"`
	NodeTypes map[string]int    `json:"nodeTypes,omitempty"`
	Category  string            `json:"category,omitempty"`
	Versions  map[string]int    `json:"versions,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

type AnalyticsFailure struct {
	Code   string `json:"code"`
	NodeID string `json:"nodeId,omitempty"`
	Count  int    `json:"count"`
}

type AnalyticsCoverage struct {
	RunsWithTelemetry int            `json:"runsWithTelemetry"`
	RunsWithLogs      int            `json:"runsWithLogs"`
	AdapterEvents     map[string]int `json:"adapterEvents"`
	LogLevels         map[string]int `json:"logLevels"`
}

type AnalyticsDelivery struct {
	IssueID      string `json:"issueId"`
	ExternalID   string `json:"externalId,omitempty"`
	ExternalIID  int    `json:"externalIid,omitempty"`
	Title        string `json:"title"`
	Source       string `json:"source"`
	IssueURL     string `json:"issueUrl,omitempty"`
	WorkflowID   string `json:"workflowId"`
	WorkflowName string `json:"workflowName"`
	RunID        string `json:"runId"`
	RunStatus    string `json:"runStatus"`
	SOPSucceeded bool   `json:"sopSucceeded"`
	// DirectDelivery marks a requirement delivered only from conversation
	// workspaces, without an SOP run: it counts as delivered once its MR merges.
	DirectDelivery bool    `json:"directDelivery,omitempty"`
	DurationMs     int64   `json:"durationMs"`
	Tokens         int64   `json:"tokens"`
	CostUSD        float64 `json:"costUsd"`
	CostCNY        float64 `json:"costCny"`
	MRURL          string  `json:"mrUrl,omitempty"`
	MRProject      string  `json:"mrProject,omitempty"`
	MRIID          int     `json:"mrIid,omitempty"`
	MRState        string  `json:"mrState,omitempty"` // merged (default branch), integrated (baseline), opened, closed, reopened, none, unknown
	// MRs retains every associated MR; the singular fields are compatibility / primary-link fields.
	MRs       []DeliveryMR `json:"mrs,omitempty"`
	Delivered bool         `json:"delivered"`

	// 该需求全部关联执行（含失败与返工）的聚合结果。
	RunCount   int `json:"runCount"`
	FailedRuns int `json:"failedRuns"`
	// Reworks 为该需求全部关联执行中的节点退回/返工次数。
	Reworks int `json:"reworks"`
	// ExecutionDurationMs 为该需求全部执行按节点求和的累计耗时。
	ExecutionDurationMs int64 `json:"executionDurationMs"`
	// CycleDurationMs 为需求首次执行开始 → 最后一次执行结束（端到端）。
	CycleDurationMs int64 `json:"cycleDurationMs"`
	// ClarificationDurationMs is the wall-clock interval from the first observed
	// clarification start to the first development node start. Development and
	// testing still sum completed execution nodes across associated Runs.
	ClarificationDurationMs int64     `json:"clarificationDurationMs"`
	DevelopmentDurationMs   int64     `json:"developmentDurationMs"`
	TestDurationMs          int64     `json:"testDurationMs"`
	ClarificationStartedAt  time.Time `json:"clarificationStartedAt,omitempty"`
	DevelopmentStartedAt    time.Time `json:"developmentStartedAt,omitempty"`
	// PhaseSamples distinguishes an observed zero duration from an uncollected phase.
	PhaseSamples AnalyticsPhaseSamples `json:"phaseSamples"`
	// PhaseEvidence links node durations and the clarification boundary to Run evidence.
	PhaseEvidence []AnalyticsPhaseEvidence `json:"phaseEvidence,omitempty"`
	// UnclassifiedPhaseNodes are completed nodes without an explicit or legacy phase.
	UnclassifiedPhaseNodes int `json:"unclassifiedPhaseNodes"`
	// FirstRunAt/LastRunAt 交付周期计算的时间边界。
	FirstRunAt time.Time `json:"firstRunAt,omitempty"`
	LastRunAt  time.Time `json:"lastRunAt,omitempty"`
	// UserID 为该需求的责任人（取最近一次执行的 ResponsibleUserID）。
	UserID string `json:"userId,omitempty"`
	// Category 来自 SOP 的受控统计标签 metricCategory。
	Category string `json:"category,omitempty"`
	// CostKnown 为 false 表示成本未识别，前端展示"未识别"而非 ¥0。
	CostKnown bool `json:"costKnown"`
	// Lanes 按产品/前端/后端等角色链拆开该需求的执行。
	Lanes []RequirementLane `json:"lanes,omitempty"`
}

type AnalyticsPhaseSamples struct {
	Clarification int `json:"clarification"`
	Development   int `json:"development"`
	Testing       int `json:"testing"`
	Inferred      int `json:"inferred"`
}

type AnalyticsPhaseEvidence struct {
	RunID      string    `json:"runId"`
	NodeID     string    `json:"nodeId"`
	Stage      string    `json:"stage"`
	Source     string    `json:"source"` // explicit or inferred
	DurationMs int64     `json:"durationMs"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

type AnalyticsDeliverySummary struct {
	Issues       int `json:"issues"`
	SOPSucceeded int `json:"sopSucceeded"`
	// DirectIssues were worked on only in conversations, without an SOP run;
	// DirectWithMR of them have submitted an MR, DirectDelivered are delivered.
	DirectIssues       int     `json:"directIssues"`
	DirectWithMR       int     `json:"directWithMr"`
	DirectDelivered    int     `json:"directDelivered"`
	WithMR             int     `json:"withMr"`
	TotalMRs           int     `json:"totalMrs"`
	MRMerged           int     `json:"mrMerged"`
	MRIntegrated       int     `json:"mrIntegrated,omitempty"`
	IssuesWithMergedMR int     `json:"issuesWithMergedMr"`
	Delivered          int     `json:"delivered"`
	AverageDurationMs  int64   `json:"averageDurationMs"`
	TotalTokens        int64   `json:"totalTokens"`
	TotalCostUSD       float64 `json:"totalCostUsd"`
	TotalCostCNY       float64 `json:"totalCostCny"`

	// CompletedIssues 按 IssueID 去重后的成功需求数（同一需求多次成功只计一次）。
	CompletedIssues int `json:"completedIssues"`
	// AverageExecutionDurationMs/IssueP50 为节点求和口径的交付周期。
	AverageExecutionDurationMs int64 `json:"averageExecutionDurationMs"`
	P50ExecutionDurationMs     int64 `json:"p50ExecutionDurationMs"`
	P95ExecutionDurationMs     int64 `json:"p95ExecutionDurationMs"`
	// RequirementCount 按 metricCategory 去重统计的需求数（如需求开发/Bug 修复）。
	RequirementCount int `json:"requirementCount"`
	BugFixCount      int `json:"bugFixCount"`
	// CostUnknownIssues 表示成本未识别的需求数。
	CostUnknownIssues int `json:"costUnknownIssues"`
}

// AnalyticsWorkflowVersion 对比同一 SOP 不同版本的执行效果。
type AnalyticsWorkflowVersion struct {
	CostUnknownRuns int    `json:"costUnknownRuns"`
	WorkflowID      string `json:"workflowId"`
	Name            string `json:"name"`
	Version         int    `json:"version"`
	Runs            int    `json:"runs"`
	Succeeded       int    `json:"succeeded"`
	Failed          int    `json:"failed"`
	// 样本量不足（runs < 5）时置 true，前端标注"样本不足"。
	InsufficientSample bool    `json:"insufficientSample"`
	SuccessRate        float64 `json:"successRate"`
	AverageDurationMs  int64   `json:"averageDurationMs"`
	// AverageExecutionDurationMs 为节点求和口径。
	AverageExecutionDurationMs int64   `json:"averageExecutionDurationMs"`
	Tokens                     int64   `json:"tokens"`
	CostUSD                    float64 `json:"costUsd"`
	CostCNY                    float64 `json:"costCny"`
}

// AnalyticsUserUsage 为"每用户每日"用量：责任人 × 自然日。
type AnalyticsUserUsage struct {
	UserID  string `json:"userId"`
	Date    string `json:"date"`
	Runs    int    `json:"runs"`
	Success int    `json:"success"`
	Failed  int    `json:"failed"`

	Tokens           int64 `json:"tokens"`
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`

	CostUSD float64 `json:"costUsd"`

	CostCNY float64 `json:"costCny"`
	// CostKnown 为 false 表示当日成本未识别。
	CostKnown bool `json:"costKnown"`

	DurationMs          int64 `json:"durationMs"`
	ExecutionDurationMs int64 `json:"executionDurationMs"`
}

// AnalyticsProjectUsage 按项目汇总用量，跨项目看个人时拆开各项目。
type AnalyticsProjectUsage struct {
	ProjectID    string  `json:"projectId"`
	Runs         int     `json:"runs"`
	Success      int     `json:"success"`
	Failed       int     `json:"failed"`
	Tokens       int64   `json:"tokens"`
	UniqueIssues int     `json:"uniqueIssues"`
	CostUSD      float64 `json:"costUsd"`
	CostCNY      float64 `json:"costCny"`
	CostKnown    bool    `json:"costKnown"`
}

// AnalyticsDemoSummary exposes the first Loop demo metrics with explicit,
// auditable denominators. Tool success only counts finished tool observations;
// automation means a successful run with no recorded human decision event.
type AnalyticsDemoSummary struct {
	CoveredRuns               int     `json:"coveredRuns"`
	CoveredSucceededRuns      int     `json:"coveredSucceededRuns"`
	AverageTokensPerRun       float64 `json:"averageTokensPerRun"`
	ToolCallsObserved         int     `json:"toolCallsObserved"`
	ToolCallsSucceeded        int     `json:"toolCallsSucceeded"`
	ToolCallsFailed           int     `json:"toolCallsFailed"`
	ToolSuccessRate           float64 `json:"toolSuccessRate"`
	HumanInterventions        int     `json:"humanInterventions"`
	AverageHumanInterventions float64 `json:"averageHumanInterventions"`
	AutomatedSucceededRuns    int     `json:"automatedSucceededRuns"`
	AutomationRate            float64 `json:"automationRate"`
}

// AnalyticsRunTrace is the compact TAPD -> workflow task -> Run -> trace row
// used by the dashboard. Event details remain cursor-paged on the Run endpoint.
type AnalyticsRunTrace struct {
	ExecutionDurationMs *int64    `json:"executionDurationMs,omitempty"`
	Origin              string    `json:"origin,omitempty"`
	SourceID            string    `json:"sourceId,omitempty"`
	WorkflowName        string    `json:"workflowName,omitempty"`
	ProjectID           string    `json:"projectId,omitempty"`
	IssueID             string    `json:"issueId,omitempty"`
	IssueSource         string    `json:"issueSource,omitempty"`
	ExternalID          string    `json:"externalId,omitempty"`
	ExternalURL         string    `json:"externalUrl,omitempty"`
	WorkflowTaskID      string    `json:"workflowTaskId,omitempty"`
	RunID               string    `json:"runId"`
	TraceID             string    `json:"traceId"`
	Status              string    `json:"status"`
	DurationMs          int64     `json:"durationMs"`
	Tokens              int64     `json:"tokens"`
	CostUSD             float64   `json:"costUsd"`
	CostCNY             float64   `json:"costCny"`
	CostKnown           bool      `json:"costKnown"`
	ToolCalls           int       `json:"toolCalls"`
	ToolCallsSucceeded  int       `json:"toolCallsSucceeded"`
	ToolCallsFailed     int       `json:"toolCallsFailed"`
	HumanInterventions  int       `json:"humanInterventions"`
	ErrorNodeID         string    `json:"errorNodeId,omitempty"`
	ErrorMessage        string    `json:"errorMessage,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
	FinishedAt          time.Time `json:"finishedAt,omitempty"`
}

// AnalyticsActivity counts persisted conversations, created requirements and
// top-level requirement SOP executions independently of token/run coverage.
type AnalyticsActivity struct {
	Sessions              int `json:"sessions"`
	CollaborativeSessions int `json:"collaborativeSessions"`
	Requirements          int `json:"requirements"`
	SOPRuns               int `json:"sopRuns"`
	// Usage is counted by reply time, independently of session creation time.
	SessionUsage       AnalyticsSessionUsage `json:"sessionUsage"`
	CollaborativeUsage AnalyticsSessionUsage `json:"collaborativeUsage"`
}

// CollaborativeUsage is a subset of SessionUsage, never an additional charge.
type AnalyticsSessionUsage struct {
	Tokens           int64 `json:"tokens"`
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	ReportedReplies  int   `json:"reportedReplies"`
	MissingReplies   int   `json:"missingReplies"`
	// UndatedReplies only participate in cumulative usage, never date buckets.
	UndatedReplies int `json:"undatedReplies"`
}

type AnalyticsActivityPoint struct {
	Date string `json:"date"`
	AnalyticsActivity
}

type AnalyticsSnapshot struct {
	ModelErrors   []AnalyticsModelError    `json:"modelErrors,omitempty"`
	Activity      AnalyticsActivity        `json:"activity"`
	ActivityTrend []AnalyticsActivityPoint `json:"activityTrend"`
	DataErrors    []string                 `json:"dataErrors,omitempty"`
	Filter        AnalyticsFilter          `json:"filter"`
	Summary       AnalyticsSummary         `json:"summary"`
	Trend         []AnalyticsTrendPoint    `json:"trend"`
	Workflows     []AnalyticsBreakdown     `json:"workflows"`
	Agents        []AnalyticsBreakdown     `json:"agents"`
	Executors     []AnalyticsBreakdown     `json:"executors"`
	Models        []AnalyticsBreakdown     `json:"models"`
	Nodes         []AnalyticsBreakdown     `json:"nodes"`
	Failures      []AnalyticsFailure       `json:"failures"`
	Coverage      AnalyticsCoverage        `json:"coverage"`
	Deliveries    []AnalyticsDelivery      `json:"deliveries,omitempty"`
	Delivery      AnalyticsDeliverySummary `json:"delivery"`
	MRMerges      []AnalyticsMRMerge       `json:"mrMerges"`

	// IssueSources 为需求类型分布（local/gongfeng/tapd）。
	IssueSources []AnalyticsBreakdown `json:"issueSources,omitempty"`
	// Categories 按 SOP 受控标签 metricCategory 拆解。
	Categories []AnalyticsBreakdown `json:"categories,omitempty"`
	// WorkflowVersions 为 SOP 版本对比。
	WorkflowVersions []AnalyticsWorkflowVersion `json:"workflowVersions,omitempty"`
	// UserUsage 为每用户每日 Token/成本/运行数。
	UserUsage []AnalyticsUserUsage `json:"userUsage,omitempty"`
	// ProjectUsage 按项目拆开当前筛选下的用量。
	ProjectUsage []AnalyticsProjectUsage `json:"projectUsage,omitempty"`
	// Demo/RunTraces provide the minimum Loop metrics loop and its drill-down index.
	Demo      AnalyticsDemoSummary `json:"demo"`
	RunTraces []AnalyticsRunTrace  `json:"runTraces,omitempty"`
	// WeekHour 按星期×小时聚合运行，时间口径与 Trend 相同，只含非零格。
	WeekHour []AnalyticsWeekHourCell `json:"weekHour,omitempty"`
}

type AnalyticsModelError struct {
	Class    string `json:"class"`
	Failures int    `json:"failures"`
	Retries  int    `json:"retries"`
}

// MatchesProject treats a non-nil empty scope as no projects, never as all projects.
func (f AnalyticsFilter) MatchesProject(id string) bool {
	if f.ProjectID != "" && f.ProjectID != id {
		return false
	}
	if f.ProjectIDs == nil {
		return true
	}
	return slices.Contains(f.ProjectIDs, id)
}

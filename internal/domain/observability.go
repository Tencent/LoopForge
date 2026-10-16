package domain

import "time"

const ObservabilitySpecVersion = "agentflow.observability/v1"

const (
	ExecutionModeCloud         = "cloud"
	ExecutionModeRunner        = "runner"
	ExecutionScopeRun          = "run"
	ExecutionScopeRepositories = "repositories"

	RunnerWorkspaceLayoutShared = "shared"
)

// TelemetrySource identifies the layer that produced an observation and the
// adapter that normalized it. Consumers must not branch on native payloads.
type TelemetrySource struct {
	Layer    string `json:"layer"` // executor, orchestrator, agent, sandbox, verification
	Adapter  string `json:"adapter"`
	Provider string `json:"provider,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}

// TelemetryContext correlates all three platform layers plus product context.
type TelemetryContext struct {
	ProjectID string `json:"projectId,omitempty"`
	IssueID   string `json:"issueId,omitempty"`
	// TAPDID is the external requirement key; local IssueID may differ across imports.
	TAPDID            string `json:"tapdId,omitempty"`
	ResponsibleUserID int64  `json:"responsibleUserId,omitempty"`
	// ExecutionMode and ExecutionRunnerID freeze the execution target when a
	// run starts. Retries reconnect to this target instead of following a
	// user's later default-runner changes.
	ExecutionMode     string `json:"executionMode,omitempty"` // cloud, runner
	ExecutionRunnerID string `json:"executionRunnerId,omitempty"`
	// ExecutionPool is "cloud" when ExecutionRunnerID was picked from the
	// platform runner pool rather than the user's default machine.
	ExecutionPool string `json:"executionPool,omitempty"`
	// RunnerWorkspaceLayout freezes how a self-hosted runner maps workflow
	// nodes to disk. Empty is the legacy per-node layout; shared gives every
	// node in a new Run the same repository and Git index.
	RunnerWorkspaceLayout string `json:"runnerWorkspaceLayout,omitempty"`
	IssueNumber           int    `json:"issueNumber,omitempty"`
	TaskID                string `json:"taskId,omitempty"`
	ScheduleJobID         string `json:"scheduleJobId,omitempty"`
	RunID                 string `json:"runId"`
	ParentRunID           string `json:"parentRunId,omitempty"`
	ParentNodeID          string `json:"parentNodeId,omitempty"`
	WorkflowID            string `json:"workflowId,omitempty"`
	WorkflowVersion       int    `json:"workflowVersion,omitempty"`
	NodeID                string `json:"nodeId,omitempty"`
	MetricStage           string `json:"metricStage,omitempty"`
	Attempt               int    `json:"attempt,omitempty"`
	AgentID               string `json:"agentId,omitempty"`
	AgentVersion          int    `json:"agentVersion,omitempty"`
	ExecutorID            string `json:"executorId,omitempty"`
	SandboxID             string `json:"sandboxId,omitempty"`
	SessionID             string `json:"sessionId,omitempty"`
	TraceID               string `json:"traceId"`
	SpanID                string `json:"spanId,omitempty"`
	ParentSpanID          string `json:"parentSpanId,omitempty"`
}

type MetricPoint struct {
	Name       string            `json:"name"`
	Value      float64           `json:"value"`
	Unit       string            `json:"unit"`
	Kind       string            `json:"kind"` // counter, gauge, histogram
	Dimensions map[string]string `json:"dimensions,omitempty"`
}

type ToolObservation struct {
	Name       string `json:"name"`
	Operation  string `json:"operation,omitempty"`
	CallID     string `json:"callId,omitempty"`
	Success    *bool  `json:"success,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

type ModelObservation struct {
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	Operation     string `json:"operation,omitempty"`
	FinishReason  string `json:"finishReason,omitempty"`
	ContextWindow int64  `json:"contextWindow,omitempty"`
}

type ErrorObservation struct {
	Code      string `json:"code,omitempty"`
	Type      string `json:"type,omitempty"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type AdapterDescriptor struct {
	ID              string   `json:"id"`
	SpecVersion     string   `json:"specVersion"`
	ExecutorKinds   []string `json:"executorKinds"`
	NativeProtocols []string `json:"nativeProtocols"`
	Capabilities    []string `json:"capabilities"`
	RawEventPolicy  string   `json:"rawEventPolicy"`
}

type ObservabilitySpec struct {
	Version           string              `json:"version"`
	Categories        []string            `json:"categories"`
	Phases            []string            `json:"phases"`
	RequiredContext   []string            `json:"requiredContext"`
	MetricConventions map[string]string   `json:"metricConventions"`
	Adapters          []AdapterDescriptor `json:"adapters"`
}

// LogRecord is the separate, cursor-addressable diagnostic channel. It shares
// correlation with telemetry but is not used for metrics aggregation.
type LogRecord struct {
	SpecVersion string           `json:"specVersion"`
	ID          string           `json:"id"`
	Sequence    int64            `json:"sequence"`
	Timestamp   time.Time        `json:"timestamp"`
	Level       string           `json:"level"`
	Stream      string           `json:"stream"` // stdout, stderr, worker, platform
	Body        string           `json:"body"`
	Source      TelemetrySource  `json:"source"`
	Context     TelemetryContext `json:"context"`
	Attributes  map[string]any   `json:"attributes,omitempty"`
}

// WorkerEnvelope is the vendor-neutral ingress contract implemented by any
// CLI, remote worker, SDK, container runner, or future execution backend.
type WorkerEnvelope struct {
	SpecVersion string          `json:"specVersion"`
	Type        string          `json:"type"` // observation, log
	Observation *TelemetryEvent `json:"observation,omitempty"`
	Log         *LogRecord      `json:"log,omitempty"`
}

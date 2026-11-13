package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Run struct {
	Kind               string             `json:"kind,omitempty"`
	ID                 string             `json:"id"`
	WorkflowID         string             `json:"workflowId"`
	WorkflowSnapshot   WorkflowDefinition `json:"workflowSnapshot"`
	SandboxID          string             `json:"sandboxId,omitempty"`
	OwnerInstanceID    string             `json:"ownerInstanceId,omitempty"`
	LeaseUntil         time.Time          `json:"leaseUntil,omitempty"`
	Generation         int                `json:"executionGeneration,omitempty"`
	WorkspaceHost      string             `json:"workspaceHost,omitempty"`
	WorkspaceEditorID  int64              `json:"workspaceEditorId,omitempty"`
	WorkspaceEditUntil time.Time          `json:"workspaceEditUntil,omitempty"`
	WorkspaceRoot      string             `json:"workspaceRoot,omitempty"`
	WorkspaceCleanedAt time.Time          `json:"workspaceCleanedAt,omitempty"`
	// WorkspaceCleanupNotBefore delays terminal workspace deletion while a
	// preview finalizer still needs the run directory. Zero means no hold.
	WorkspaceCleanupNotBefore time.Time         `json:"workspaceCleanupNotBefore,omitempty"`
	Status                    string            `json:"status"`
	AdmissionState            string            `json:"admissionState,omitempty"`
	QueueEnteredAt            time.Time         `json:"queueEnteredAt,omitempty"`
	ConcurrencyExempt         bool              `json:"concurrencyExempt,omitempty"`
	CancelRequested           bool              `json:"cancelRequested"`
	PauseRequested            bool              `json:"pauseRequested"`
	Input                     string            `json:"input"`
	Repository                RepositoryContext `json:"repository,omitempty"`
	// Repositories lists every repository frozen into this run. Repository
	// mirrors the first entry so old readers (UI cards, tasks) keep working;
	// execution logic must iterate Repositories and treats all entries as
	// equals. Legacy runs only carry Repository.
	Repositories      []RepositoryContext `json:"repositories,omitempty"`
	ExecutionContext  TelemetryContext    `json:"executionContext,omitempty"`
	Nodes             map[string]*NodeRun `json:"nodes"`
	Events            []RunEvent          `json:"events"`
	Telemetry         []TelemetryEvent    `json:"telemetry,omitempty"`
	TelemetrySequence int64               `json:"telemetrySequence"`
	Logs              []LogRecord         `json:"logs,omitempty"`
	LogSequence       int64               `json:"logSequence"`
	Usage             TokenUsage          `json:"usage"`
	ToolCalls         int                 `json:"toolCalls"`
	Artifacts         []Artifact          `json:"artifacts,omitempty"`
	ArtifactCount     int                 `json:"artifactCount,omitempty"`
	HumanContext      string              `json:"humanContext,omitempty"`
	Timing            *RunTiming          `json:"timing,omitempty"`
	CreatedAt         time.Time           `json:"createdAt"`
	UpdatedAt         time.Time           `json:"updatedAt"`
	Revision          int                 `json:"revision"`
}

const RunKindWorkspace = "workspace"
const ConversationWorkspaceWorkflowID = "conversation-workspace"

// IsWorkspace distinguishes an on-demand chat workspace from a scheduled SOP.
func (r Run) IsWorkspace() bool { return r.Kind == RunKindWorkspace }

// LiveWorkspace reports a chat workspace that is open for edits. It has no SOP
// graph to pause, so Main Bot and people in the IDE edit it side by side and
// rely on expectedSha256 to catch conflicting writes.
func (r Run) LiveWorkspace() bool {
	return r.IsWorkspace() && !r.CancelRequested && r.WorkspaceCleanedAt.IsZero() && (r.Status == "running" || r.Status == "waiting")
}

// HumanEditable reports whether people may edit the workspace in the IDE. Like
// an AI IDE they edit at any time, also while SOP agents run: nothing pauses,
// expectedSha256 catches conflicting writes and agents re-read what changed.
func (r Run) HumanEditable() bool {
	if !r.WorkspaceCleanedAt.IsZero() {
		return false
	}
	switch r.Status {
	case "succeeded", "failed", "cancelled":
		return true
	case "running", "waiting":
		return !r.CancelRequested
	}
	return false
}

// HistoricalGroup is a record of the retired role groups. It has no SOP graph
// that could be resumed or retried.
func (r Run) HistoricalGroup() bool {
	return !r.IsWorkspace() && r.WorkflowSnapshot.CollaborationMode == "group"
}

// RepositoryContexts returns the frozen repositories of this run. New runs
// fill Repositories; legacy runs only carry the single Repository field.
func (r Run) RepositoryContexts() []RepositoryContext {
	if len(r.Repositories) > 0 {
		return r.Repositories
	}
	if !r.Repository.IsZero() {
		return []RepositoryContext{r.Repository}
	}
	return nil
}

// UnmarshalJSON lifts the shared sandbox instance from historical node runs.
// NodeRun.SandboxID remains readable during the engine transition, but new
// clients consume the run-level field.
func (r *Run) UnmarshalJSON(raw []byte) error {
	type runAlias Run
	var value runAlias
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*r = Run(value)
	if r.SandboxID != "" {
		return nil
	}
	for _, node := range r.Nodes {
		if node == nil || strings.TrimSpace(node.SandboxID) == "" {
			continue
		}
		if r.SandboxID != "" && r.SandboxID != node.SandboxID {
			return fmt.Errorf("run %q has conflicting legacy node sandbox ids %q and %q", r.ID, r.SandboxID, node.SandboxID)
		}
		r.SandboxID = node.SandboxID
	}
	return nil
}

type NodeRun struct {
	NodeID          string         `json:"nodeId"`
	Status          string         `json:"status"`
	Attempt         int            `json:"attempt"`
	ReworkCount     int            `json:"reworkCount"`
	SandboxID       string         `json:"sandboxId,omitempty"`
	Output          string         `json:"output,omitempty"`
	Error           string         `json:"error,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	Model           string         `json:"model,omitempty"`
	SessionID       string         `json:"sessionId,omitempty"`
	ResumeSessionID string         `json:"resumeSessionId,omitempty"`
	Activity        string         `json:"activity,omitempty"`
	Feedback        string         `json:"feedback,omitempty"`
	Usage           TokenUsage     `json:"usage"`
	UsageSessionID  string         `json:"usageSessionId,omitempty"`
	UsageCursor     TokenUsage     `json:"usageCursor,omitempty"`
	ToolCalls       int            `json:"toolCalls"`
	GateResult      *GateResult    `json:"gateResult,omitempty"`
	RouteDecision   *RouteDecision `json:"routeDecision,omitempty"`
	SkipReason      string         `json:"skipReason,omitempty"`
	ChildRunID      string         `json:"childRunId,omitempty"`
	StartedAt       time.Time      `json:"startedAt,omitzero"`
	FinishedAt      time.Time      `json:"finishedAt,omitzero"`
}

// GateResult is the structured terminal decision submitted by an Agent through
// the invocation-scoped submit_gate_result MCP tool.
type GateResult struct {
	Decision       string        `json:"decision"` // pass, rework, fail, human
	Reason         string        `json:"reason"`
	ActionRequired string        `json:"actionRequired,omitempty"`
	Summary        string        `json:"summary,omitempty"`
	Findings       []GateFinding `json:"findings,omitempty"`
	Route          RouteValues   `json:"route,omitempty"`
}

// RouteValues accepts either a single label or a list from an agent or script.
type RouteValues []string

func (r *RouteValues) UnmarshalJSON(raw []byte) error {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		*r = RouteValues{single}
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	*r = values
	return nil
}

// SelectedEdges is keyed by the edge's index in the frozen workflow snapshot.
type RouteDecision struct {
	Values        RouteValues     `json:"values,omitempty"`
	SelectedEdges map[string]bool `json:"selectedEdges"`
}

type GateFinding struct {
	Severity string `json:"severity,omitempty"`
	Title    string `json:"title"`
	Evidence string `json:"evidence,omitempty"`
}

type TokenUsage struct {
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	CostUSD          float64 `json:"costUsd"`
	ReasoningTokens  int64   `json:"reasoningTokens,omitempty"`
	ContextWindow    int64   `json:"contextWindow,omitempty"`
	// CostStatus 区分"成本为 0"与"成本未知"：reported（适配器上报）、estimated（按 token 估算）、
	// unknown（未识别，展示为"未识别"而不是 ¥0）。空值视为 unknown，旧数据向后兼容。
	CostStatus string `json:"costStatus,omitempty"`
}

type TelemetryEvent struct {
	SpecVersion    string            `json:"specVersion"`
	ID             string            `json:"id"`
	RunID          string            `json:"runId"`
	NodeID         string            `json:"nodeId,omitempty"`
	Sequence       int64             `json:"sequence"`
	Timestamp      time.Time         `json:"timestamp"`
	Category       string            `json:"category"` // lifecycle, model, reasoning, output, tool, usage, process, verification, sandbox
	Name           string            `json:"name"`
	Phase          string            `json:"phase,omitempty"` // start, progress, finish
	Level          string            `json:"level,omitempty"`
	Message        string            `json:"message,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	Model          string            `json:"model,omitempty"`
	SessionID      string            `json:"sessionId,omitempty"`
	ToolCallID     string            `json:"toolCallId,omitempty"`
	ParentID       string            `json:"parentId,omitempty"`
	Usage          *TokenUsage       `json:"usage,omitempty"`
	Attributes     map[string]any    `json:"attributes,omitempty"`
	Source         TelemetrySource   `json:"source"`
	Context        TelemetryContext  `json:"context"`
	Metrics        []MetricPoint     `json:"metrics,omitempty"`
	Tool           *ToolObservation  `json:"tool,omitempty"`
	ModelOperation *ModelObservation `json:"modelOperation,omitempty"`
	Error          *ErrorObservation `json:"error,omitempty"`
}

type RunEvent struct {
	ID        string    `json:"id"`
	RunID     string    `json:"runId"`
	NodeID    string    `json:"nodeId,omitempty"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// Artifact is an immutable, attempt-scoped snapshot. Keeping old attempts makes
// rework auditable while downstream nodes consume the newest successful version.
type Artifact struct {
	ID        string `json:"id"`
	RunID     string `json:"runId"`
	NodeID    string `json:"nodeId"`
	Attempt   int    `json:"attempt"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Content   string `json:"content"`
	Stale     bool   `json:"stale,omitempty"` // derived from the current routing attempt
	// RepositoryDir records which run repository workspace the artifact was
	// collected from (empty = run workspace root, where SOP deliverables live).
	RepositoryDir string    `json:"repositoryDir,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// MarkStaleArtifacts retains history while identifying outputs from inactive attempts.
func (r *Run) MarkStaleArtifacts() {
	conditional := false
	for _, edge := range r.WorkflowSnapshot.Edges {
		if edge.Condition != nil {
			conditional = true
			break
		}
	}
	if !conditional {
		return
	}
	for i := range r.Artifacts {
		a := &r.Artifacts[i]
		n := r.Nodes[a.NodeID]
		a.Stale = n == nil || n.Status != "succeeded" || n.Attempt != a.Attempt
	}
}

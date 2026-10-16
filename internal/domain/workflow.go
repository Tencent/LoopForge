package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type VerificationGate struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Mode      string   `json:"mode"`            // command, manual
	Stage     string   `json:"stage,omitempty"` // after_execution, before_publish
	Commands  []string `json:"commands,omitempty"`
	Required  bool     `json:"required"`
	OnFailure string   `json:"onFailure,omitempty"` // loop, reject, fail, human
	// ExecutionScope is required for command gates: run or repositories.
	ExecutionScope string `json:"executionScope,omitempty"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// TaskPromptSpec is a portable taskbook. The control plane compiles it into
// executor-neutral Markdown before handing it to any worker implementation.
type TaskPromptSpec struct {
	Role               string   `json:"role,omitempty"`
	Objective          string   `json:"objective,omitempty"`
	Context            string   `json:"context,omitempty"`
	Inputs             []string `json:"inputs,omitempty"`
	Actions            []string `json:"actions,omitempty"`
	Deliverables       []string `json:"deliverables,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria,omitempty"`
	Constraints        []string `json:"constraints,omitempty"`
	RetryInstructions  string   `json:"retryInstructions,omitempty"`
}

// WorkflowRequirement declares an external capability a SOP needs before it can
// deliver end to end. Studio renders the list as a preflight checklist when a
// project adopts a template, so a missing 工蜂 Git Token or 工蜂 MCP config
// surfaces before the first run instead of failing in the middle of a pipeline.
type WorkflowRequirement struct {
	Key    string `json:"key"`              // stable id: gongfeng-git-token, gongfeng-mcp, taihu-pat
	NodeID string `json:"nodeId,omitempty"` // node that depends on the capability
	Label  string `json:"label"`
	Hint   string `json:"hint,omitempty"`
	Setup  string `json:"setup,omitempty"` // settings (personal credentials) | mcp (MCP management)
}

type WorkflowNode struct {
	TaskPrompt   string   `json:"taskPrompt,omitempty"`
	RequestedBy  string   `json:"requestedBy,omitempty"`
	RequestID    string   `json:"requestId,omitempty"`
	ClaimedFiles []string `json:"claimedFiles,omitempty"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Type         string   `json:"type"` // agent, script, approval, sop
	// MetricStage explicitly assigns this node's execution time to a Loop phase.
	// Empty means a legacy workflow without a classification; "none" opts out.
	MetricStage string `json:"metricStage,omitempty"` // clarification, development, testing, none
	AgentID     string `json:"agentId,omitempty"`
	ExecutorID  string `json:"executorId,omitempty"`
	// SOPWorkflowID is the referenced workflow when Type is "sop".
	SOPWorkflowID string `json:"sopWorkflowId,omitempty"`
	// RunOnWorkspaceRebuild replays this script when restoring an already-used environment.
	RunOnWorkspaceRebuild bool               `json:"runOnWorkspaceRebuild,omitempty"`
	ScriptLanguage        string             `json:"scriptLanguage,omitempty"` // shell, python
	Script                string             `json:"script,omitempty"`
	ExecutionScope        string             `json:"executionScope,omitempty"` // run, repositories; empty = run
	Command               string             `json:"command,omitempty"`        // legacy command-node payload
	ArtifactOutputs       []ArtifactContract `json:"artifactOutputs,omitempty"`
	RejectTo              string             `json:"rejectTo,omitempty"`
	OnFailure             string             `json:"onFailure,omitempty"`     // fail, reject
	RetryStrategy         string             `json:"retryStrategy,omitempty"` // targeted_fix, full_redo
	CarryOver             []string           `json:"carryOver,omitempty"`
	MaxAttempts           int                `json:"maxAttempts"`
	MaxReworks            int                `json:"maxReworks"`
	Notifications         []NodeNotification `json:"notifications,omitempty"`
	Position              Position           `json:"position"`
}

// ValidateWorkflowMetricStages prevents silently dropping a mistyped stage.
func ValidateWorkflowMetricStages(workflow WorkflowDefinition) error {
	for _, node := range workflow.Nodes {
		switch node.MetricStage {
		case "", "clarification", "development", "testing", "none":
		default:
			return fmt.Errorf("workflow node %q has unsupported metricStage %q", node.ID, node.MetricStage)
		}
	}
	return nil
}

// ArtifactContract is the declared output contract of a workflow node. Paths are
// relative to the node workspace; Markdown is the default interchange format.
type ArtifactContract struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	MediaType string `json:"mediaType,omitempty"`
	Required  bool   `json:"required"`
}

type WorkflowEdge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	When   string `json:"when"` // success
}

// DefinitionRevision is an immutable snapshot of a workflow or agent so
// operators can diff and restore a previous version.
type DefinitionRevision struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"` // workflow, agent
	EntityID  string          `json:"entityId"`
	Version   int             `json:"version"`
	Revision  int             `json:"revision"`
	Name      string          `json:"name"`
	Summary   string          `json:"summary,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

// SOPCheckpoint is frozen in the run JSON, never in a catalog definition.
type SOPCheckpoint struct {
	Workflow WorkflowDefinition  `json:"workflow"`
	Nodes    map[string]*NodeRun `json:"nodes"`
}
type GroupTaskHistory struct {
	Node  WorkflowNode `json:"node"`
	State NodeRun      `json:"state"`
}

func (w WorkflowDefinition) GroupMemberIDs() []string {
	if w.GroupMembers != nil {
		return w.GroupMembers
	}
	// Legacy groups retain only roles actually associated with their frozen graph.
	ids := []string{}
	seen := map[string]bool{}
	for _, n := range w.Nodes {
		if n.AgentID != "" && !seen[n.AgentID] {
			ids = append(ids, n.AgentID)
			seen[n.AgentID] = true
		}
	}
	return ids
}
func (w WorkflowDefinition) HasGroupMember(id string) bool {
	for _, member := range w.GroupMemberIDs() {
		if member == id {
			return true
		}
	}
	return false
}

// GroupSOPHandoff points at immutable, archived context for resumed SOP nodes.
// It belongs to a run snapshot, never to the catalog template.
type GroupSOPHandoff struct {
	ID                   string    `json:"id"`
	NodeID               string    `json:"nodeId"`
	SummaryPath          string    `json:"summaryPath"`
	TranscriptPath       string    `json:"transcriptPath"`
	SummaryArtifactID    string    `json:"summaryArtifactId"`
	TranscriptArtifactID string    `json:"transcriptArtifactId"`
	CreatedAt            time.Time `json:"createdAt"`
}

type WorkflowDefinition struct {
	GroupSOPHandoff   *GroupSOPHandoff      `json:"groupSopHandoff,omitempty"`
	GroupMembers      []string              `json:"groupMembers,omitempty"`
	SOPCheckpoint     *SOPCheckpoint        `json:"sopCheckpoint,omitempty"`
	GroupHistory      []GroupTaskHistory    `json:"groupHistory,omitempty"`
	CollaborationMode string                `json:"collaborationMode,omitempty"`
	TaskTitle         string                `json:"taskTitle,omitempty"`
	Notifications     []NotificationBinding `json:"notifications,omitempty"` // SOP completion reports
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	Version           int                   `json:"version"`
	Revision          int                   `json:"revision"`
	Status            string                `json:"status"` // draft, published, archived
	ProjectID         string                `json:"projectId,omitempty"`
	Shared            bool                  `json:"shared,omitempty"`
	Owner             string                `json:"owner,omitempty"`
	Tags              []string              `json:"tags,omitempty"`
	Description       string                `json:"description"`
	SandboxProfileID  string                `json:"sandboxProfileId"`
	// MetricCategory 是受控的统计标签（需求开发/Bug 修复/…），用于交付指标归类。
	// Run 创建时随 WorkflowSnapshot 冻结，历史执行不受后续编辑影响。
	MetricCategory string `json:"metricCategory,omitempty"`
	// SOPLane 是受控的角色链标签（产品/前端/后端/修复），一条需求上可并列多条。
	// 和 MetricCategory 正交：category 回答「需求还是 Bug」，lane 回答「哪条角色链」。
	SOPLane      string                `json:"sopLane,omitempty"`
	Generic      bool                  `json:"generic,omitempty"`
	Requirements []WorkflowRequirement `json:"requirements,omitempty"`
	// SandboxSnapshot is populated only on Run.WorkflowSnapshot. It freezes
	// the resolved profile plus Dockerfile/environment payload so retries can
	// reconstruct the original environment after catalog definitions change.
	SandboxSnapshot *RunnerSandboxSpec `json:"sandboxSnapshot,omitempty"`
	// Preview is the optional frontend delivery contract. It is frozen into
	// the run snapshot so later SOP edits cannot change an in-flight archive.
	Preview   *PreviewContract `json:"preview,omitempty"`
	Nodes     []WorkflowNode   `json:"nodes"`
	Edges     []WorkflowEdge   `json:"edges"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// UnmarshalJSON migrates the pre-workflow-sandbox shape while the legacy node
// field is still visible. Conflicting node profiles cannot describe one shared
// workflow sandbox and are rejected instead of being silently guessed.
func (w *WorkflowDefinition) UnmarshalJSON(raw []byte) error {
	type workflowAlias WorkflowDefinition
	type legacyNode struct {
		WorkflowNode
		SandboxProfileID      string `json:"sandboxProfileId,omitempty"`
		RunOnWorkspaceRebuild *bool  `json:"runOnWorkspaceRebuild,omitempty"`
		LegacyPreparation     string `json:"workspacePreparation,omitempty"`
	}
	var value struct {
		workflowAlias
		Nodes []legacyNode `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*w = WorkflowDefinition(value.workflowAlias)
	w.Nodes = make([]WorkflowNode, len(value.Nodes))
	legacyProfile := ""
	for i, node := range value.Nodes {
		w.Nodes[i] = node.WorkflowNode
		if node.RunOnWorkspaceRebuild != nil {
			w.Nodes[i].RunOnWorkspaceRebuild = *node.RunOnWorkspaceRebuild
		} else if node.LegacyPreparation == "environment" || node.LegacyPreparation == "code" {
			// Read earlier local prototypes as the single opt-in flag. New JSON only writes the flag.
			w.Nodes[i].RunOnWorkspaceRebuild = true
		}
		profile := strings.TrimSpace(node.SandboxProfileID)
		if profile == "" {
			continue
		}
		if legacyProfile != "" && profile != legacyProfile {
			return fmt.Errorf("workflow %q has conflicting legacy node sandbox profiles %q and %q", w.ID, legacyProfile, profile)
		}
		legacyProfile = profile
	}
	if w.SandboxProfileID == "" {
		w.SandboxProfileID = legacyProfile
	} else if legacyProfile != "" && w.SandboxProfileID != legacyProfile {
		return fmt.Errorf("workflow %q sandbox profile %q conflicts with legacy node profile %q", w.ID, w.SandboxProfileID, legacyProfile)
	}
	return nil
}

type WorkflowMetrics struct {
	WorkflowID      string               `json:"workflowId"`
	TotalRuns       int                  `json:"totalRuns"`
	ActiveRuns      int                  `json:"activeRuns"`
	WaitingRuns     int                  `json:"waitingRuns"`
	SucceededRuns   int                  `json:"succeededRuns"`
	FailedRuns      int                  `json:"failedRuns"`
	CancelledRuns   int                  `json:"cancelledRuns"`
	ReworkedRuns    int                  `json:"reworkedRuns"`
	ClosureRate     float64              `json:"closureRate"`
	SuccessRate     float64              `json:"successRate"`
	ReworkRate      float64              `json:"reworkRate"`
	AverageDuration int64                `json:"averageDurationMs"`
	TotalTokens     int64                `json:"totalTokens"`
	TotalToolCalls  int                  `json:"totalToolCalls"`
	TotalArtifacts  int                  `json:"totalArtifacts"`
	LastRunAt       *time.Time           `json:"lastRunAt,omitempty"`
	LastRunStatus   string               `json:"lastRunStatus,omitempty"`
	RecentRuns      []RunSummary         `json:"recentRuns,omitempty"`
	NodeMetrics     []WorkflowNodeMetric `json:"nodeMetrics,omitempty"`
}

type RunSummary struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	DurationMs  int64     `json:"durationMs"`
	ReworkCount int       `json:"reworkCount"`
	TotalTokens int64     `json:"totalTokens"`
	ToolCalls   int       `json:"toolCalls"`
	Artifacts   int       `json:"artifacts"`
}

type WorkflowNodeMetric struct {
	NodeID          string  `json:"nodeId"`
	Name            string  `json:"name"`
	Executions      int     `json:"executions"`
	Succeeded       int     `json:"succeeded"`
	Failed          int     `json:"failed"`
	Waiting         int     `json:"waiting"`
	Reworks         int     `json:"reworks"`
	SuccessRate     float64 `json:"successRate"`
	AverageDuration int64   `json:"averageDurationMs"`
}

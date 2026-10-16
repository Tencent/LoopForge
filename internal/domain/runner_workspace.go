package domain

import (
	"encoding/json"
	"time"
)

const (
	RunnerWorkspaceOperationArtifacts         = "restore_artifacts" // additive, never replaces existing files
	RunnerWorkspaceOperationReclaim           = "reclaim"           // service-only worker reclamation
	RunnerWorkspaceOperationCheckpoint        = "checkpoint"
	RunnerWorkspaceOperationRestore           = "restore"
	RunnerWorkspaceOperationPrepare           = "prepare"
	RunnerWorkspaceOperationPreparationStatus = "preparation_status"
	RunnerWorkspaceOperationPreparationRecord = "preparation_record"
	RunnerWorkspaceOperationTerminal          = "terminal"
	RunnerWorkspaceOperationWrite             = "write"
	RunnerWorkspaceOperationInfo              = "info"
	RunnerWorkspaceOperationTree              = "tree"
	RunnerWorkspaceOperationFile              = "file"
	RunnerWorkspaceOperationDiff              = "diff"
	RunnerWorkspaceOperationSearch            = "search"
	RunnerWorkspaceOperationLog               = "log"
	RunnerWorkspaceOperationCommit            = "commit"
)

// RunnerWorkspaceRequest is a bounded workspace request sent over the
// runner's outbound control socket. Responses are transient and are never
// persisted in runner_tasks or the run record.
type RunnerWorkspaceRequest struct {
	// Sandbox and Workflow are resolved exclusively from the frozen run by the
	// control plane. A terminal must never silently substitute a host shell.
	PreparationNodeID     string                    `json:"preparationNodeId,omitempty"`
	PreparationOutput     string                    `json:"preparationOutput,omitempty"`
	PreparationInvocation *RunnerInvocation         `json:"preparationInvocation,omitempty"`
	Sandbox               *RunnerSandboxSpec        `json:"sandbox,omitempty"`
	Workflow              *WorkflowDefinition       `json:"workflow,omitempty"`
	ArtifactFiles         []WorkspaceArtifactFile   `json:"artifactFiles,omitempty"`
	CheckpointLease       string                    `json:"checkpointLease,omitempty"`
	Checkpoint            []byte                    `json:"checkpoint,omitempty"`
	CollaborationMode     string                    `json:"collaborationMode,omitempty"`
	Terminal              *WorkspaceTerminalRequest `json:"terminal,omitempty"`
	TerminalOwner         int64                     `json:"terminalOwner,omitempty"`
	RunID                 string                    `json:"runId"`
	RunStatus             string                    `json:"runStatus,omitempty"`
	WorkspaceLayout       string                    `json:"workspaceLayout,omitempty"`
	Operation             string                    `json:"operation"`
	Content               string                    `json:"content,omitempty"`
	ExpectedSHA256        string                    `json:"expectedSha256,omitempty"`
	Path                  string                    `json:"path,omitempty"`
	Query                 string                    `json:"query,omitempty"`
	Ref                   string                    `json:"ref,omitempty"`
	Repositories          []RepositoryContext       `json:"repositories,omitempty"`
}

type RunnerWorkspaceResponse struct {
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
	Status  int             `json:"status,omitempty"`
}

// WorkspaceTerminalRequest carries transient PTY data, never durable chat or run logs.
type WorkspaceTerminalRequest struct {
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	Operation string    `json:"operation"`
	ID        string    `json:"id,omitempty"`
	Data      string    `json:"data,omitempty"`
	Cols      int       `json:"cols,omitempty"`
	Rows      int       `json:"rows,omitempty"`
}

// WorkspaceArtifactFile is an immutable archived output, restored only if absent.
type WorkspaceArtifactFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

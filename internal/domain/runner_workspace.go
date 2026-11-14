package domain

import (
	"encoding/json"
	"time"
)

const (
	RunnerWorkspaceOperationCheckpointStatus      = "checkpoint_status"
	RunnerWorkspaceOperationCheckpointExport      = "checkpoint_export"
	RunnerWorkspaceOperationCheckpointRead        = "checkpoint_read"
	RunnerWorkspaceOperationCheckpointClose       = "checkpoint_close"
	RunnerWorkspaceOperationCheckpointUpload      = "checkpoint_upload"
	RunnerWorkspaceOperationCheckpointAppend      = "checkpoint_append"
	RunnerWorkspaceOperationCheckpointCommit      = "checkpoint_commit"
	RunnerWorkspaceOperationArtifacts             = "restore_artifacts" // additive, never replaces existing files
	RunnerWorkspaceOperationReclaim               = "reclaim"           // service-only worker reclamation
	RunnerWorkspaceOperationCheckpoint            = "checkpoint"
	RunnerWorkspaceOperationRestore               = "restore"
	RunnerWorkspaceOperationPrepare               = "prepare"
	RunnerWorkspaceOperationPreparationStatus     = "preparation_status"
	RunnerWorkspaceOperationPreparationRecord     = "preparation_record"
	RunnerWorkspaceOperationTerminal              = "terminal"
	RunnerWorkspaceOperationWrite                 = "write"
	RunnerWorkspaceOperationInfo                  = "info"
	RunnerWorkspaceOperationTree                  = "tree"
	RunnerWorkspaceOperationFile                  = "file"
	RunnerWorkspaceOperationDiff                  = "diff"
	RunnerWorkspaceOperationSearch                = "search"
	RunnerWorkspaceOperationLog                   = "log"
	RunnerWorkspaceOperationCommit                = "commit"
	RunnerWorkspaceOperationBrowser               = "browser"
	RunnerWorkspaceOperationNodeCheckpointCreate  = "node_checkpoint_create"
	RunnerWorkspaceOperationNodeCheckpointRestore = "node_checkpoint_restore"
	RunnerWorkspaceOperationNodeCheckpointDiff    = "node_checkpoint_diff"
	RunnerWorkspaceOperationNodeCheckpointRelease = "node_checkpoint_release"
	RunnerWorkspaceOperationNodeCheckpointReserve = "node_checkpoint_reserve"
)

// RunnerWorkspaceRequest is a bounded workspace request sent over the
// runner's outbound control socket. Responses are transient and are never
// persisted in runner_tasks or the run record.
type RunnerWorkspaceRequest struct {
	NodeCheckpoint         *NodeCheckpoint `json:"nodeCheckpoint,omitempty"`
	NodeCheckpointEnd      *NodeCheckpoint `json:"nodeCheckpointEnd,omitempty"`
	NodeCheckpointAttempt  int             `json:"nodeCheckpointAttempt,omitempty"`
	DiscardNodeCheckpoints bool            `json:"discardNodeCheckpoints,omitempty"`
	RunKind                string          `json:"runKind,omitempty"`
	// ConcurrentEdit marks a person editing from the IDE: the runner writes and
	// opens terminals beside running tasks instead of waiting for their fence.
	ConcurrentEdit         bool                         `json:"concurrentEdit,omitempty"`
	NodeCheckpointsEnabled bool                         `json:"nodeCheckpointsEnabled,omitempty"`
	CheckpointTransfer     *WorkspaceCheckpointTransfer `json:"checkpointTransfer,omitempty"`
	// Only the control plane may allow a clean checkout to be refreshed, before
	// any non-preparation node has run. Later nodes keep their frozen baseline.
	RefreshCleanRepositories bool `json:"refreshCleanRepositories,omitempty"`
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
	Terminal              *WorkspaceTerminalRequest `json:"terminal,omitempty"`
	TerminalOwner         int64                     `json:"terminalOwner,omitempty"`
	TerminalGitSSHKeys    map[string]string         `json:"terminalGitSshKeys,omitempty"`
	// WorkspaceRunID selects the shared checkout owner; RunID continues to
	// identify this run's independent sandbox, terminal scope and locks.
	Browser         *BrowserRequest     `json:"browser,omitempty"`
	WorkspaceRunID  string              `json:"workspaceRunId,omitempty"`
	RunID           string              `json:"runId"`
	RunStatus       string              `json:"runStatus,omitempty"`
	WorkspaceLayout string              `json:"workspaceLayout,omitempty"`
	Operation       string              `json:"operation"`
	Content         string              `json:"content,omitempty"`
	ExpectedSHA256  string              `json:"expectedSha256,omitempty"`
	Path            string              `json:"path,omitempty"`
	Query           string              `json:"query,omitempty"`
	Ref             string              `json:"ref,omitempty"`
	Repositories    []RepositoryContext `json:"repositories,omitempty"`
}

type WorkspaceCheckpointTransfer struct {
	ID     string `json:"id,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Data   []byte `json:"data,omitempty"`
}

func IsWorkspaceCheckpointTransferOperation(op string) bool {
	switch op {
	case RunnerWorkspaceOperationCheckpointExport, RunnerWorkspaceOperationCheckpointRead, RunnerWorkspaceOperationCheckpointClose, RunnerWorkspaceOperationCheckpointUpload, RunnerWorkspaceOperationCheckpointAppend, RunnerWorkspaceOperationCheckpointCommit:
		return true
	}
	return false
}

type RunnerWorkspaceResponse struct {
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
	Code    string          `json:"code,omitempty"`
	Status  int             `json:"status,omitempty"`
}

const (
	WorkspaceErrorBusy    = "workspace_busy"
	WorkspaceErrorMissing = "workspace_missing"
	// WorkspaceErrorTerminalActive: an exclusive human terminal holds the
	// workspace; waiting does not help until the user closes it.
	WorkspaceErrorTerminalActive = "workspace_terminal_active"
)

// WorkspaceRestoreStatus describes restoration separately from SOP execution.
type WorkspaceRestoreStatus struct {
	Status    string     `json:"status"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	Error     string     `json:"error,omitempty"`
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

// BrowserRequest drives the run's agent browser from the task panel. Frames
// and input are transient and are never persisted.
type BrowserRequest struct {
	Operation string `json:"operation"`
	After     int64  `json:"after,omitempty"`
	WaitMs    int    `json:"waitMs,omitempty"`
	URL       string `json:"url,omitempty"`
	TargetID  string `json:"targetId,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	// FrameWidth caps panel frames at the device pixels the panel shows, so
	// a wide viewport is not shipped at full size to a narrow dock.
	FrameWidth int                 `json:"frameWidth,omitempty"`
	Events     []BrowserInputEvent `json:"events,omitempty"`
	// Agent-driven operations (snapshot/click/type/press) address elements by
	// the refs returned in the latest snapshot.
	Ref    string `json:"ref,omitempty"`
	Text   string `json:"text,omitempty"`
	Key    string `json:"key,omitempty"`
	Submit bool   `json:"submit,omitempty"`
	// WithSnapshot returns the page snapshot after a navigation settles.
	WithSnapshot bool `json:"withSnapshot,omitempty"`
	// Ensure starts the run browser when none is running yet.
	Ensure bool `json:"ensure,omitempty"`
	// HTTP carries one request to a dev server port inside the run sandbox
	// (operation "http"), so the user's own browser renders the page natively.
	HTTP *BrowserHTTPRequest `json:"http,omitempty"`
	// File is the absolute path of a workspace file as the run browser sees
	// it (operation "file"), served so the user's browser renders file://
	// pages natively. The response comes back in BrowserState.HTTP.
	File string `json:"file,omitempty"`
	// Point is where the panel inspects the page (operation "inspect"), as a
	// fraction of the viewport like input events; Detail asks for the full
	// element description rather than just its box and label.
	Point  *BrowserPoint `json:"point,omitempty"`
	Detail bool          `json:"detail,omitempty"`
}

// BrowserPoint is a position as a fraction of the viewport (0..1).
type BrowserPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// BrowserInspection is the element under a point of the Agent browser page.
// Rect is in CSS pixels of the top-level viewport.
type BrowserInspection struct {
	Label    string                `json:"label"`
	Rect     ElementRect           `json:"rect"`
	Viewport ElementRect           `json:"viewport"`
	Element  *AssistantPageElement `json:"element,omitempty"`
}

// BrowserHTTPRequest is one proxied request to http://localhost:Port in the
// run's network namespace. Path keeps the query string.
type BrowserHTTPRequest struct {
	Port   int                 `json:"port"`
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}

// BrowserHTTPResponse is the dev server's answer to a BrowserHTTPRequest.
type BrowserHTTPResponse struct {
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}

// BrowserInputEvent coordinates are normalized to the frame (0..1).
type BrowserInputEvent struct {
	Type       string  `json:"type"`
	Action     string  `json:"action,omitempty"`
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	Button     string  `json:"button,omitempty"`
	Buttons    int     `json:"buttons,omitempty"`
	ClickCount int     `json:"clickCount,omitempty"`
	Modifiers  int     `json:"modifiers,omitempty"`
	DeltaX     float64 `json:"deltaX,omitempty"`
	DeltaY     float64 `json:"deltaY,omitempty"`
	Key        string  `json:"key,omitempty"`
	Code       string  `json:"code,omitempty"`
	KeyCode    int     `json:"keyCode,omitempty"`
	Text       string  `json:"text,omitempty"`
}

type BrowserTab struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Active bool   `json:"active,omitempty"`
}

type BrowserState struct {
	Available bool    `json:"available"`
	Reason    string  `json:"reason,omitempty"`
	Kind      string  `json:"kind,omitempty"`
	Seq       int64   `json:"seq"`
	Frame     string  `json:"frame,omitempty"`
	Width     float64 `json:"width,omitempty"`
	Height    float64 `json:"height,omitempty"`
	URL       string  `json:"url,omitempty"`
	Title     string  `json:"title,omitempty"`
	// ScrollX/Y and ContentWidth/Height (CSS px) bound how far the panel may
	// slide a frame ahead of the page when it predicts a scroll.
	ScrollX       float64          `json:"scrollX,omitempty"`
	ScrollY       float64          `json:"scrollY,omitempty"`
	ContentWidth  float64          `json:"contentWidth,omitempty"`
	ContentHeight float64          `json:"contentHeight,omitempty"`
	CanGoBack     bool             `json:"canGoBack,omitempty"`
	CanGoForward  bool             `json:"canGoForward,omitempty"`
	Tabs          []BrowserTab     `json:"tabs,omitempty"`
	Snapshot      *BrowserSnapshot `json:"snapshot,omitempty"`
	// HTTP answers operation "http"; Reason explains a missing answer.
	HTTP *BrowserHTTPResponse `json:"http,omitempty"`
	// Inspect answers operation "inspect"; nil when nothing is at the point.
	Inspect *BrowserInspection `json:"inspect,omitempty"`
}

// BrowserSnapshot is a text view of the active page for language models.
type BrowserSnapshot struct {
	Text      string           `json:"text"`
	Truncated bool             `json:"truncated,omitempty"`
	Elements  []BrowserElement `json:"elements"`
}

type BrowserElement struct {
	Ref      string `json:"ref"`
	Role     string `json:"role"`
	Name     string `json:"name,omitempty"`
	Value    string `json:"value,omitempty"`
	Checked  bool   `json:"checked,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// WorkspaceArtifactFile is an immutable archived output, restored only if absent.
type WorkspaceArtifactFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

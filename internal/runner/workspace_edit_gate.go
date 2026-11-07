package runner

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"agentflow/internal/domain"
)

// Runner-side fencing remains valid if the API request times out: a task cannot
// start using this run's workspace until the actual file operation has ended.
//
// The key is the run that owns the workspace directory, not the requesting
// run: a child SOP executes in its parent's checkout, so its tasks must fence
// the parent's preparation, restore and snapshot (and vice versa).
func (d *Daemon) workspaceGate(key string) *sync.RWMutex {
	gate, _ := d.workspaceGates.LoadOrStore(key, &sync.RWMutex{})
	return gate.(*sync.RWMutex)
}

// requestGateKey resolves the same owner runnerWorkspaceRoot resolves.
func requestGateKey(request domain.RunnerWorkspaceRequest) string {
	if request.WorkspaceLayout == domain.RunnerWorkspaceLayoutShared {
		if owner := strings.TrimSpace(request.WorkspaceRunID); owner != "" {
			return owner
		}
	}
	return strings.TrimSpace(request.RunID)
}

func (d *Daemon) requestGate(request domain.RunnerWorkspaceRequest) *sync.RWMutex {
	return d.workspaceGate(requestGateKey(request))
}

// taskGateKey derives the owner from the directory the task actually runs in:
// runs/<owner>/workspace for the shared layout, runs/<run>/<node> for legacy
// per-node workspaces. Both resolve to the run directory a request fences.
func (d *Daemon) taskGateKey(task domain.RunnerTask) string {
	if root, err := filepath.Abs(d.cfg.DefaultWorkspaceRoot); err == nil {
		if workspace, err := filepath.Abs(d.taskWorkspace(task)); err == nil {
			if rel, err := filepath.Rel(root, workspace); err == nil {
				parts := strings.Split(filepath.ToSlash(rel), "/")
				if len(parts) >= 2 && parts[0] == "runs" && parts[1] != "" && parts[1] != "." && parts[1] != ".." {
					return parts[1]
				}
			}
		}
	}
	return task.RunID
}

// workspaceBusyError marks a transient fence conflict: the caller may retry
// once the current holder (task, preparation, snapshot) releases the gate.
type workspaceBusyError struct{ message string }

func (e *workspaceBusyError) Error() string { return e.message }

func newWorkspaceBusyError(message string) error { return &workspaceBusyError{message: message} }

func isWorkspaceBusyError(err error) bool {
	var busy *workspaceBusyError
	return errors.As(err, &busy)
}

func workspaceBusyResponse(message string) domain.RunnerWorkspaceResponse {
	return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorBusy, Error: message}
}

// workspaceTerminalActiveResponse is not retryable by waiting: a human holds
// the workspace and has to close their terminal first.
func workspaceTerminalActiveResponse(message string) domain.RunnerWorkspaceResponse {
	return domain.RunnerWorkspaceResponse{Status: 409, Code: domain.WorkspaceErrorTerminalActive, Error: message}
}

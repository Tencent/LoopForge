package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// runnerWorkspaceBusy reports a transient fence conflict on the runner: another
// task, preparation or snapshot holds the workspace and will release it.
// Older runners carry no code field, so their fixed messages still count.
func runnerWorkspaceBusy(response domain.RunnerWorkspaceResponse) bool {
	if response.Code == domain.WorkspaceErrorBusy {
		return true
	}
	if response.Code != "" || response.Status != 409 {
		return false
	}
	switch response.Error {
	case "workspace is still in use", "workspace preparation is in progress", "execution is still stopping":
		return true
	}
	return strings.HasPrefix(response.Error, "workspace is in use;") ||
		strings.HasPrefix(response.Error, "workspace preparation is in progress;")
}

// errWorkspaceTerminalActive: waiting does not help; a person has to close the
// exclusive terminal they opened in the workspace.
var errWorkspaceTerminalActive = errors.New("工作区终端仍处于打开状态，请先关闭终端再继续")

// runnerWorkspaceResponseError classifies a failed runner workspace response.
// Busy conflicts wrap repo.ErrWorkspaceInUse so HTTP callers answer "busy,
// retry" and the scheduler waits instead of failing the node.
func runnerWorkspaceResponseError(response domain.RunnerWorkspaceResponse) error {
	if response.Code == domain.WorkspaceErrorTerminalActive {
		return fmt.Errorf("%w（%s）", errWorkspaceTerminalActive, response.Error)
	}
	if runnerWorkspaceBusy(response) {
		return fmt.Errorf("%w（%s）", repo.ErrWorkspaceInUse, response.Error)
	}
	return fmt.Errorf("%s", response.Error)
}

// nodePreparationBusyWait bounds how long a starting node waits for another
// holder of the workspace. It covers one full runner-side preparation (15 min)
// so a parallel branch or group role never fails just because a sibling is
// preparing the same environment.
var nodePreparationBusyWait = 16 * time.Minute

const (
	nodePreparationBusyRetryMin = 300 * time.Millisecond
	nodePreparationBusyRetryMax = 3 * time.Second
)

// waitWorkspaceNotBusy repeats attempt while it reports a busy workspace. The
// run is reloaded before every retry so a plan built from succeeded nodes sees
// siblings that finished meanwhile.
func (e *Engine) waitWorkspaceNotBusy(ctx context.Context, run domain.Run, attempt func(domain.Run) (runtime.WorkspacePreparationStatus, error)) (runtime.WorkspacePreparationStatus, error) {
	deadline := time.Now().Add(nodePreparationBusyWait)
	delay := nodePreparationBusyRetryMin
	for {
		state, err := attempt(run)
		if err == nil || !errors.Is(err, repo.ErrWorkspaceInUse) {
			return state, err
		}
		if !time.Now().Before(deadline) {
			return state, fmt.Errorf("等待工作区空闲超时（%s）：%w", nodePreparationBusyWait, err)
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-time.After(delay):
		}
		if delay *= 2; delay > nodePreparationBusyRetryMax {
			delay = nodePreparationBusyRetryMax
		}
		if current, ok := e.repo.RunState(run.ID); ok {
			run = current
		}
	}
}

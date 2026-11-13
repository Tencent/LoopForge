package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// executeNodeCheckpointCreate prepares the initial checkout before capturing
// it. The daemon's task fence covers checkout and capture together.
func (d *Daemon) executeNodeCheckpointCreate(ctx context.Context, task domain.RunnerTask) execResult {
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start checkpoint: %v", err)}
	}
	key := d.taskGateKey(task)
	if task.NodeCheckpointRunID != key || d.workspaceTerminals.HasRunScope(key) || d.workspaceTerminals.HasRunScope(task.RunID) {
		return execResult{status: domain.RunnerTaskFailed, errMsg: "workspace terminal active or owner mismatch"}
	}
	if task.NodeCheckpointLease == "" {
		return execResult{status: domain.RunnerTaskFailed, errMsg: "checkpoint workspace lease is required"}
	}
	// StartTask checked the token against the durable workspace lease. This
	// task holds the exclusive runner gate, so a stale reservation can safely
	// be replaced without racing an old writer on this daemon.
	d.nodeCheckpointReservations.Store(key, task.NodeCheckpointLease)
	workspace := d.taskWorkspace(task)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}
	}
	if err := checkoutInvocationRepositories(ctx, workspace, task.Invocation, runnerTaskSandboxDriver(task)); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("repository checkout: %v", err)}
	}
	var repositories []domain.RepositoryContext
	if task.Invocation != nil {
		repos := task.Invocation.Repositories
		if len(repos) == 0 && task.Invocation.Repository != nil {
			repos = []domain.RunnerRepository{*task.Invocation.Repository}
		}
		for _, repo := range repos {
			repositories = append(repositories, domain.RepositoryContext{RepositoryID: repo.RepositoryID, WorkspaceDir: repo.Dir})
		}
	}
	checkpoint, err := runtime.CreateNodeWorkspaceCheckpoint(ctx, workspace, repositories, task.NodeCheckpointAttempt)
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}
	}
	return execResult{status: domain.RunnerTaskSuccess, result: string(payload)}
}

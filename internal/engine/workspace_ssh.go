package engine

import (
	"context"
	"fmt"
	"io"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/workspacessh"
)

func (e *Engine) WorkspaceSupportsSSH(run domain.Run) bool {
	if e.foreignWorkspaceBind(run) {
		return false
	}
	profile, _, err := e.workspaceSandboxProfile(run)
	return err == nil && profile.Driver == domain.SandboxDriverDocker
}

// OpenPreparedWorkspaceSSH is called after the workspace has been restored
// and prepared, while the caller still holds the short preparation lease.
func (e *Engine) OpenPreparedWorkspaceSSH(ctx context.Context, runID string, keys workspacessh.Keys) (io.ReadWriteCloser, error) {
	e.runEnvMu.Lock()
	env := e.runEnvs[runID]
	e.runEnvMu.Unlock()
	if env == nil {
		return nil, fmt.Errorf("工作区环境尚未准备")
	}
	if !env.nodeMu.TryLock() {
		return nil, repo.ErrWorkspaceInUse
	}
	defer env.nodeMu.Unlock()
	return runtime.OpenWorkspaceSSH(ctx, env.sandbox, keys)
}

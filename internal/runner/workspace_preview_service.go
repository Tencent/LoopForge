package runner

import (
	"context"
	"net/http"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func (d *Daemon) workspacePreviewService(ctx context.Context, root string, request domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
	req := request.PreviewService
	if req == nil {
		return domain.RunnerWorkspaceResponse{Status: http.StatusBadRequest, Error: "invalid preview service request"}
	}
	if err := req.Validate(); err != nil {
		return domain.RunnerWorkspaceResponse{Status: http.StatusBadRequest, Error: err.Error()}
	}
	if d.cfg.DisableRunSandboxReuse {
		return domain.RunnerWorkspaceResponse{Status: http.StatusConflict, Error: "持续预览需要执行机器启用工作区沙箱复用"}
	}
	task, err := d.workspaceSandboxTask(root, request)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	gate := d.requestGate(request)
	if !gate.TryRLock() {
		return workspaceBusyResponse("workspace is still in use")
	}
	defer gate.RUnlock()
	sb, release, err := d.acquireTaskSandbox(ctx, task)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	defer release()
	state, err := runtime.WorkspacePreviewService(ctx, sb, *req)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	return runnerWorkspacePayload(state)
}

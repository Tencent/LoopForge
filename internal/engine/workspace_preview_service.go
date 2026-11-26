package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// WorkspacePreviewService operates on the same frozen sandbox that serves
// the workspace browser. The caller holds the conversation workspace lease.
func (e *Engine) WorkspacePreviewService(ctx context.Context, run domain.Run, req domain.WorkspacePreviewServiceRequest) (domain.WorkspacePreviewServiceState, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if !e.foreignWorkspaceBind(run) && e.defaultExecutionRunner(run) == "" {
		return e.localWorkspacePreviewService(ctx, run, req)
	}
	var state domain.WorkspacePreviewServiceState
	response, err := e.ForwardWorkspaceRequest(ctx, run, domain.RunnerWorkspaceRequest{
		Operation: domain.RunnerWorkspaceOperationPreviewService, PreviewService: &req,
	})
	if err != nil {
		return state, err
	}
	if response.Error != "" {
		if strings.Contains(response.Error, "unsupported workspace operation") {
			return state, errors.New("执行机器版本过旧，请升级后再使用持续预览")
		}
		return state, errors.New(response.Error)
	}
	err = json.Unmarshal(response.Payload, &state)
	return state, err
}

func (e *Engine) localWorkspacePreviewService(ctx context.Context, run domain.Run, req domain.WorkspacePreviewServiceRequest) (domain.WorkspacePreviewServiceState, error) {
	if err := req.Validate(); err != nil {
		return domain.WorkspacePreviewServiceState{}, err
	}
	env, err := e.acquireConversationEnvironment(ctx, run)
	if err != nil {
		return domain.WorkspacePreviewServiceState{}, err
	}
	return runtime.WorkspacePreviewService(ctx, env.sandbox, req)
}

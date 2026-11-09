package engine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// HandleBrowserRequest serves the run's agent browser on the instance hosting
// its workspace. Runner-executed runs are relayed over the runner transport.
func (e *Engine) HandleBrowserRequest(ctx context.Context, run domain.Run, req *domain.BrowserRequest) domain.RunnerWorkspaceResponse {
	if req == nil {
		return domain.RunnerWorkspaceResponse{Status: 400, Error: "invalid browser request"}
	}
	if e.defaultExecutionRunner(run) != "" {
		if e.WorkspaceCheckpointTransport == nil {
			return domain.RunnerWorkspaceResponse{Status: 503, Error: "runner transport unavailable"}
		}
		request, err := e.BrowserWorkspaceRequest(run, req)
		if err != nil {
			payload, _ := json.Marshal(domain.BrowserState{Reason: err.Error()})
			return domain.RunnerWorkspaceResponse{Payload: payload}
		}
		response, err := e.WorkspaceCheckpointTransport(ctx, run, request)
		if err != nil {
			return domain.RunnerWorkspaceResponse{Status: 503, Error: err.Error()}
		}
		return response
	}
	if req.Ensure && !runtime.BrowserRunning(ctx, run.ID) {
		if err := e.ensureRunBrowser(ctx, run); err != nil {
			payload, _ := json.Marshal(domain.BrowserState{Reason: err.Error()})
			return domain.RunnerWorkspaceResponse{Payload: payload}
		}
	}
	state, err := runtime.HandleBrowser(ctx, run.ID, *req)
	payload, _ := json.Marshal(state)
	response := domain.RunnerWorkspaceResponse{Payload: payload}
	if err != nil {
		response.Error, response.Status = err.Error(), 409
	}
	return response
}

// ForwardBrowserRequest reaches the worker that owns a foreign workspace bind.
func (e *Engine) ForwardBrowserRequest(ctx context.Context, run domain.Run, req *domain.BrowserRequest) (domain.RunnerWorkspaceResponse, error) {
	return e.requestWorkspaceCheckpoint(ctx, run, browserWorkspaceRequest(run, req))
}

func browserWorkspaceRequest(run domain.Run, req *domain.BrowserRequest) domain.RunnerWorkspaceRequest {
	return domain.RunnerWorkspaceRequest{RunID: run.ID, RunStatus: run.Status, WorkspaceLayout: run.ExecutionContext.RunnerWorkspaceLayout, Operation: domain.RunnerWorkspaceOperationBrowser, Browser: req}
}

// BrowserWorkspaceRequest builds the runner request for a browser call. A
// start request carries the frozen run sandbox, so the runner launches the
// browser inside the same sandbox the run's agents use.
func (e *Engine) BrowserWorkspaceRequest(run domain.Run, req *domain.BrowserRequest) (domain.RunnerWorkspaceRequest, error) {
	request := browserWorkspaceRequest(run, req)
	if !req.Ensure {
		return request, nil
	}
	return e.preparedWorkspaceRequest(run, request)
}

// ensureRunBrowser starts the browser in the run's shared sandbox, so Main Bot
// and the stage agents drive the same browser the user watches.
func (e *Engine) ensureRunBrowser(ctx context.Context, run domain.Run) error {
	if !runtime.BrowserEnabled() {
		return errors.New("平台已关闭 Agent 浏览器")
	}
	// Starting from the panel counts as conversation use: it restores a
	// reclaimed workspace and leaves the run to the idle conversation cleanup,
	// which reclaims this browser with the sandbox.
	if ConversationWorkspaceToken(ctx, run.ID) == "" {
		token, err := e.BeginConversationWorkspaceUse(run.ID)
		if err != nil {
			return err
		}
		defer func() {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_ = e.EndConversationWorkspaceUse(saveCtx, run.ID, token)
		}()
		if _, err := e.PrepareConversationWorkspace(ctx, run.ID, token); err != nil {
			return err
		}
		ctx = WithConversationWorkspaceToken(ctx, token, run.ID)
	}
	env, err := e.acquireConversationEnvironment(ctx, run)
	if err != nil {
		return err
	}
	if runtime.EnsureSandboxBrowser(ctx, env.sandbox, env.Workspace(), run.ID, nil) == nil {
		return errors.New("Agent 浏览器启动失败：当前运行环境不支持，或 Chromium 不可用")
	}
	return nil
}

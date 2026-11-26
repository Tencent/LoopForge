package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWorkspacePreviewServiceToolRoutesToBoundRunner(t *testing.T) {
	h, _, session, gateway, ids := workspaceTurnFixture(t)
	session.RuntimeMode = domain.AssistantModeWorkspace
	session.ExpiresAt = time.Now().Add(time.Minute)
	const runnerID = "preview-service-runner"
	if err := h.repo.PutRunner(domain.Runner{ID: runnerID, RunnerVersion: domain.RunnerVersionConversationCLI, Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	run, ok := h.repo.RunState(ids[0])
	if !ok {
		t.Fatal("workspace is missing")
	}
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
	run.ExecutionContext.ExecutionRunnerID = runnerID
	run.ExecutionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
	run.WorkflowSnapshot.SandboxSnapshot = &domain.RunnerSandboxSpec{
		Profile: domain.SandboxProfile{Driver: domain.SandboxDriverDocker},
	}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	requests := make(chan domain.RunnerWorkspaceRequest, 2)
	h.engine.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		switch req.Operation {
		case domain.RunnerWorkspaceOperationPrepare:
			return domain.RunnerWorkspaceResponse{}, nil
		case domain.RunnerWorkspaceOperationPreviewService:
			requests <- req
			return domain.RunnerWorkspaceResponse{Payload: json.RawMessage(`{"name":"frontend","status":"running"}`)}, nil
		default:
			return domain.RunnerWorkspaceResponse{}, errors.New("no snapshot in this fixture")
		}
	}
	ctx := context.Background()
	tool, ok := lookupAgentTool(gateway.buildCatalog(ctx, session), workspacePreviewServiceToolName)
	if !ok {
		t.Fatal("bound workspace did not expose the preview tool")
	}
	args := map[string]any{"operation": "start", "name": "frontend", "command": "npm run dev"}
	out, err := gateway.dispatch(ctx, session, tool, args)
	if err != nil || !strings.Contains(out, `"status":"running"`) {
		t.Fatalf("preview tool output %q: %v", out, err)
	}
	select {
	case req := <-requests:
		if req.RunID != ids[0] || req.Sandbox == nil || req.Sandbox.Profile.Driver != domain.SandboxDriverDocker ||
			req.PreviewService == nil || req.PreviewService.Name != "frontend" || req.PreviewService.Command != "npm run dev" {
			t.Fatalf("invalid preview request: %+v", req)
		}
	default:
		t.Fatal("preview request did not reach the bound runner")
	}
	args["command"] = "git push origin HEAD"
	if _, err = gateway.dispatch(ctx, session, tool, args); err == nil || !strings.Contains(err.Error(), "审批") || len(requests) != 0 {
		t.Fatalf("the preview tool accepted an external write: %v", err)
	}
}

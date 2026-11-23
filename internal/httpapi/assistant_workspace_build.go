package httpapi

import (
	"context"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

const assistantDockerBuildTool = "workspace_docker_build"

func withAssistantDockerBuildProgress(ctx context.Context, session *AgentToolSession) context.Context {
	if session.Notify == nil || runtime.DockerBuildProgressObserver(ctx) != nil {
		return ctx
	}
	return runtime.WithDockerBuildProgress(ctx, func(update domain.WorkspaceImageBuildProgress) {
		if ctx.Err() != nil || update.ID == "" {
			return
		}
		id := "docker-build-" + update.ID
		at := time.Now().UTC()
		if update.Done {
			session.Notify("thought", assistantStreamEvent{Kind: "toolDone", ID: id, At: &at, Failed: update.Failed})
			return
		}
		session.Notify("thought", assistantStreamEvent{Kind: "tool", Tool: assistantDockerBuildTool, ID: id, At: &at, Detail: update.Step})
	})
}

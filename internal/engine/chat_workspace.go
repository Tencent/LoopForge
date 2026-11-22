package engine

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// StartConversationWorkspace allocates an idle workspace, never an SOP scheduler
// or a group of role tasks. Docker definitions are frozen by the normal run builder.
// A local workspace opens the project's repositories as worktrees.
func (e *Engine) StartConversationWorkspace(input string, actor domain.TelemetryContext, sandboxID string) (domain.Run, error) {
	if strings.TrimSpace(input) == "" {
		return domain.Run{}, fmt.Errorf("请填写工作区目标")
	}
	actor.ProjectID = strings.TrimSpace(actor.ProjectID)
	if actor.ProjectID == "" {
		if actor.ResponsibleUserID <= 0 {
			return domain.Run{}, fmt.Errorf("个人工作区需要登录用户")
		}
	} else if _, ok := e.repo.Project(actor.ProjectID); !ok {
		return domain.Run{}, fmt.Errorf("项目不存在")
	}
	profile, err := e.repo.ConversationSandbox(actor.ProjectID, sandboxID)
	if err != nil {
		return domain.Run{}, err
	}
	var repositories []domain.RepositoryContext
	if profile.Driver == domain.SandboxDriverLocal {
		repositories = e.localWorkspaceRepositories(actor.ProjectID)
	}
	wf := domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, Name: "对话工作区", Version: 1, ProjectID: actor.ProjectID, SandboxProfileID: sandboxID, Nodes: []domain.WorkflowNode{}, Edges: []domain.WorkflowEdge{}}
	return e.startWorkflowDefinition(wf, input, repositories, actor, true)
}

// localWorkspaceRepositories lists the project's repositories a local
// workspace opens as worktrees, each on a fresh work branch.
func (e *Engine) localWorkspaceRepositories(projectID string) []domain.RepositoryContext {
	var out []domain.RepositoryContext
	for _, r := range e.repo.Repositories() {
		if r.ProjectID != projectID || strings.TrimSpace(r.CloneURL) == "" {
			continue
		}
		out = append(out, domain.RepositoryContext{
			RepositoryID: r.ID, Provider: r.Provider, Name: r.Name, GongfengProjectID: r.GongfengProjectID,
			WebURL: r.WebURL, CloneURL: r.CloneURL, BaseBranch: r.DefaultBranch, DefaultBranch: r.DefaultBranch,
			PipelineProjectID: r.PipelineProjectID, PipelineDefID: r.PipelineDefID, PipelineURLPattern: r.PipelineURLPattern,
		})
	}
	return out
}

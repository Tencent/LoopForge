package engine

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// StartConversationWorkspace allocates an idle workspace, never an SOP scheduler
// or a group of role tasks. Docker definitions are frozen by the normal run builder.
func (e *Engine) StartConversationWorkspace(input string, repositories []domain.RepositoryContext, actor domain.TelemetryContext, sandboxID string) (domain.Run, error) {
	if strings.TrimSpace(actor.ProjectID) == "" || strings.TrimSpace(input) == "" {
		return domain.Run{}, fmt.Errorf("请选择项目并填写工作区目标")
	}
	if _, ok := e.repo.Project(actor.ProjectID); !ok {
		return domain.Run{}, fmt.Errorf("项目不存在")
	}
	if _, err := e.repo.ConversationSandbox(actor.ProjectID, sandboxID); err != nil {
		return domain.Run{}, err
	}
	wf := domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, Name: "对话工作区", Version: 1, ProjectID: actor.ProjectID, SandboxProfileID: sandboxID, Nodes: []domain.WorkflowNode{}, Edges: []domain.WorkflowEdge{}}
	return e.startWorkflowDefinition(wf, input, repositories, actor, true)
}

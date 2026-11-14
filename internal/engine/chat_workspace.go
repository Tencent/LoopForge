package engine

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// StartConversationWorkspace allocates an idle workspace, never an SOP scheduler
// or a group of role tasks. Docker definitions are frozen by the normal run builder.
func (e *Engine) StartConversationWorkspace(input string, repositories []domain.RepositoryContext, actor domain.TelemetryContext, sandboxID string) (domain.Run, error) {
	if strings.TrimSpace(input) == "" {
		return domain.Run{}, fmt.Errorf("请填写工作区目标")
	}
	if strings.TrimSpace(actor.ProjectID) == "" {
		if actor.ResponsibleUserID <= 0 || len(repositories) != 0 {
			return domain.Run{}, fmt.Errorf("个人工作区需要有效用户，且不能检出项目仓库")
		}
	} else if _, ok := e.repo.Project(actor.ProjectID); !ok {
		return domain.Run{}, fmt.Errorf("项目不存在")
	}
	if _, err := e.repo.ConversationSandbox(actor.ProjectID, sandboxID); err != nil {
		return domain.Run{}, err
	}
	wf := domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, Name: "对话工作区", Version: 1, ProjectID: actor.ProjectID, SandboxProfileID: sandboxID, Nodes: []domain.WorkflowNode{}, Edges: []domain.WorkflowEdge{}}
	return e.startWorkflowDefinition(wf, input, repositories, actor, true)
}

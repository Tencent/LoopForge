package engine

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// Each SOP checks only its own assets at admission. A parent node binding
// authorizes traversal, not access to another project's private dependencies.
// The sandbox carries its own Dockerfile or environment variables, so checking
// the sandbox covers its configuration.
func (e *Engine) validateSharedScheduleWorkflow(wf domain.WorkflowDefinition, projectID string, profile domain.SandboxProfile) error {
	if err := sharedScheduleAssetVisible(projectID, "SOP", wf.ProjectID, wf.Shared || wf.Generic); err != nil {
		return err
	}
	if err := sharedScheduleAssetVisible(projectID, "沙箱", profile.ProjectID, profile.Shared); err != nil {
		return err
	}
	for _, node := range wf.Nodes {
		if node.Type != "agent" || domain.IsPreviewBuildAgent(node.AgentID) {
			continue
		}
		agent, ok := workflowAgent(e.repo, wf, node.AgentID)
		if !ok {
			return fmt.Errorf("共享任务使用的角色不存在")
		}
		if err := sharedScheduleAssetVisible(projectID, "角色", agent.ProjectID, agent.Shared); err != nil {
			return err
		}
	}
	return nil
}

func sharedScheduleAssetVisible(projectID, kind, owner string, shared bool) error {
	projectID, owner = strings.TrimSpace(projectID), strings.TrimSpace(owner)
	if shared || projectID != "" && owner == projectID {
		return nil
	}
	return fmt.Errorf("共享任务使用的%s不属于当前项目且尚未共享", kind)
}

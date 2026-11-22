package repo

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

// PutAgentWithSharing preserves the catalog save path: a shared agent shares
// its owned rules and skills before its definition is saved.
func (r *CatalogRepo) PutAgentWithSharing(v domain.AgentDefinition) error {
	if v.Shared {
		if err := r.ShareAgentAssets(v); err != nil {
			return err
		}
	}
	return r.PutAgent(v)
}

// ShareAgent shares a stored agent through the same boundary as catalog saves.
// Already-shared agents are reused without replaying their dependency writes.
func (r *CatalogRepo) ShareAgent(id string) error {
	v, ok := r.Agent(id)
	if !ok {
		return fmt.Errorf("agent %q not found", id)
	}
	if v.Shared {
		return nil
	}
	v.Shared = true
	return r.PutAgentWithSharing(v)
}

// ShareAgentAssets is the existing catalog policy for direct agent bindings.
func (r *CatalogRepo) ShareAgentAssets(agent domain.AgentDefinition) error {
	for _, id := range agent.Rules {
		item, ok := r.Rule(id)
		if !ok || item.Shared || (item.ProjectID != "" && item.ProjectID != agent.ProjectID) {
			continue
		}
		item.Shared = true
		if err := r.PutRule(item); err != nil {
			return fmt.Errorf("share rule %q: %w", id, err)
		}
	}
	for _, id := range agent.Skills {
		item, ok := r.Skill(id)
		if !ok || item.Shared || (item.ProjectID != "" && item.ProjectID != agent.ProjectID) {
			continue
		}
		item.Shared = true
		if err := r.PutSkill(item); err != nil {
			return fmt.Errorf("share skill %q: %w", id, err)
		}
	}
	return nil
}

// ShareSkill keeps the stored definition and file manifest intact when only
// its sharing state changes. Skill file contents remain in their own table.
func (r *CatalogRepo) ShareSkill(id string) error {
	v, ok := r.Skill(id)
	if !ok {
		return fmt.Errorf("skill %q not found", id)
	}
	if v.Shared {
		return nil
	}
	v.Shared = true
	return r.PutSkill(v)
}

// ValidateWorkflowAgentScopes is shared by SOP saves and sharing transitions.
func (r *CatalogRepo) ValidateWorkflowAgentScopes(workflow domain.WorkflowDefinition) error {
	projectID := strings.TrimSpace(workflow.ProjectID)
	if projectID == "" {
		return nil
	}
	for _, node := range workflow.Nodes {
		if node.Type != "agent" {
			continue
		}
		agentID := strings.TrimSpace(node.AgentID)
		if agentID == "" || domain.IsPreviewBuildAgent(agentID) {
			continue
		}
		agent, ok := r.Agent(agentID)
		if !ok {
			return fmt.Errorf("角色 %q 不存在", agentID)
		}
		owner := strings.TrimSpace(agent.ProjectID)
		if owner == "" || (owner != projectID && !agent.Shared) {
			return fmt.Errorf("角色 %q 不属于当前项目且尚未共享", agent.Name)
		}
	}
	return nil
}

// ValidateWorkflowSharing preserves the SOP sandbox boundary. Sharing a SOP
// requires an already-shared sandbox; it does not share the sandbox itself.
func (r *CatalogRepo) ValidateWorkflowSharing(v domain.WorkflowDefinition) error {
	if strings.TrimSpace(v.SandboxProfileID) == "" {
		return fmt.Errorf("workflow sandboxProfileId is required")
	}
	profile, ok := r.Sandbox(v.SandboxProfileID)
	if !ok {
		return fmt.Errorf("sandbox profile not found")
	}
	if v.ProjectID != "" && profile.ProjectID != "" && profile.ProjectID != v.ProjectID && !profile.Shared {
		return fmt.Errorf("沙箱不属于当前项目且尚未共享")
	}
	if v.Shared && !profile.Shared {
		return fmt.Errorf("共享 SOP 只能使用已共享的沙箱")
	}
	return nil
}

// ShareWorkflow applies SOP sharing checks before the ordinary save path,
// including its existing notification and revision validation.
func (r *CatalogRepo) ShareWorkflow(id string) error {
	v, ok := r.Workflow(id)
	if !ok {
		return fmt.Errorf("workflow %q not found", id)
	}
	if v.Shared || v.Generic {
		return nil
	}
	v.Shared = true
	if err := r.ValidateWorkflowAgentScopes(v); err != nil {
		return err
	}
	if err := r.ValidateWorkflowSharing(v); err != nil {
		return err
	}
	return r.PutWorkflow(v)
}

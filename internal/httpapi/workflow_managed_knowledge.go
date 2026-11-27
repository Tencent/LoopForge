package httpapi

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// managedKnowledgeRefsUnsupported returns why a managed Agent (WMA/A2A) role
// cannot consume knowledge bindings. Management-side knowledge lives on the
// remote agent, so the engine only mounts knowledge MCP servers on the CLI
// path; a binding accepted here would be saved, run green, and silently
// ignored. Rejecting it mirrors the role-level KnowledgeBase rule in
// AgentDefinition.ValidateManagedExecutor.
func managedKnowledgeRefsUnsupported(agent domain.AgentDefinition, provider, nodeName string, refs []domain.KnowledgeRef) error {
	if !domain.IsManagedAgentExecutor(provider) || len(refs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ConnectorID)
	}
	name := strings.TrimSpace(nodeName)
	if name == "" {
		name = agent.Name
	}
	return fmt.Errorf("节点 %q 使用了托管 Agent %q，不支持关联知识库（%s）；请在托管 Agent 的服务端配置检索，或把该节点改用 CLI 执行器",
		name, provider, strings.Join(ids, "、"))
}

// workflowManagedKnowledgeRole resolves the role a node runs as, preferring the
// role's executor over the node override exactly like the engine does when it
// routes a node to the managed path. Generic templates keep their packaged
// roles outside the catalog, so those are resolved from the template.
func workflowManagedKnowledgeRole(store *repo.Store, w domain.WorkflowDefinition, node domain.WorkflowNode) (domain.AgentDefinition, string, bool) {
	agentID := strings.TrimSpace(node.AgentID)
	if agentID == "" || domain.IsPreviewBuildAgent(agentID) {
		return domain.AgentDefinition{}, "", false
	}
	agent, ok := store.Agent(agentID)
	if !ok && w.Generic {
		if template, found := store.GenericTemplateByWorkflow(w.ID); found {
			for _, role := range template.Agents {
				if role.ID == agentID {
					agent, ok = role, true
					break
				}
			}
		}
	}
	if !ok {
		return domain.AgentDefinition{}, "", false
	}
	provider := agent.ExecutorID
	if provider == "" {
		provider = node.ExecutorID
	}
	return agent, provider, true
}

// validateWorkflowManagedKnowledge rejects knowledge bindings attached to
// nodes whose role runs as a managed Agent.
func validateWorkflowManagedKnowledge(store *repo.Store, w domain.WorkflowDefinition) error {
	for _, node := range w.Nodes {
		if node.Type != "agent" || len(node.KnowledgeRefs) == 0 {
			continue
		}
		agent, provider, ok := workflowManagedKnowledgeRole(store, w, node)
		if !ok {
			continue
		}
		if err := managedKnowledgeRefsUnsupported(agent, provider, node.Name, node.KnowledgeRefs); err != nil {
			return err
		}
	}
	return nil
}

// validateRunManagedKnowledge rejects a run-level knowledgeRefs pin when the
// workflow contains a managed Agent node: that node can never honour the pin,
// so the run would otherwise finish with the knowledge silently missing.
func validateRunManagedKnowledge(store *repo.Store, w domain.WorkflowDefinition, refs []domain.KnowledgeRef) error {
	if len(refs) == 0 {
		return nil
	}
	for _, node := range w.Nodes {
		if node.Type != "agent" {
			continue
		}
		agent, provider, ok := workflowManagedKnowledgeRole(store, w, node)
		if !ok {
			continue
		}
		if err := managedKnowledgeRefsUnsupported(agent, provider, node.Name, refs); err != nil {
			return fmt.Errorf("本次运行指定了知识库，但它对整条流程生效：%w", err)
		}
	}
	return nil
}

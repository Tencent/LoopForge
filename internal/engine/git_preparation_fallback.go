package engine

import "agentflow/internal/domain"

// Only an initial Agent can prepare code on behalf of the platform. Script
// nodes need a checkout before starting and cannot act on a prompt. Approval
// and environment-preparation nodes do not consume this first-Agent slot.
func firstRepositoryAgent(wf domain.WorkflowDefinition, node domain.WorkflowNode) bool {
	if node.Type != "agent" || node.RunOnWorkspaceRebuild {
		return false
	}
	byID := map[string]domain.WorkflowNode{}
	for _, n := range wf.Nodes {
		byID[n.ID] = n
	}
	seen := map[string]bool{}
	var initial func(string) bool
	initial = func(id string) bool {
		if seen[id] {
			return true
		}
		seen[id] = true
		for _, edge := range wf.Edges {
			if edge.Target != id {
				continue
			}
			parent, ok := byID[edge.Source]
			if !ok {
				continue
			}
			if !parent.RunOnWorkspaceRebuild && parent.Type != "approval" {
				return false
			}
			if !initial(parent.ID) {
				return false
			}
		}
		return true
	}
	return initial(node.ID)
}

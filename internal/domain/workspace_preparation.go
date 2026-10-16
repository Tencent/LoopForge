package domain

import "fmt"

// ValidateWorkflowPreparation validates the optional rebuild replay flag.
// A flagged node remains an ordinary SOP script with its original dependencies.
func ValidateWorkflowPreparation(workflow WorkflowDefinition) error {
	for _, node := range workflow.Nodes {
		if node.RunOnWorkspaceRebuild && node.Type != "script" && node.Type != "command" {
			return fmt.Errorf("node %s: only script nodes may run on workspace rebuild", node.ID)
		}
	}
	return nil
}

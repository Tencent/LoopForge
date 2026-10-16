package runeval

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

func ApplyRecommendation(rec domain.RunEvalRecommendation, workflow *domain.WorkflowDefinition, agent *domain.AgentDefinition) error {
	action := domain.NormalizeRunEvalAction(rec.Target, rec.Action)
	switch action {
	case "workflow.update_node":
		if workflow == nil {
			return fmt.Errorf("workflow is required")
		}
		return updateWorkflowNode(workflow, rec)
	case "agent.upsert_gate":
		if agent == nil {
			return fmt.Errorf("agent is required")
		}
		return upsertAgentGate(agent, rec, false)
	case "agent.remove_gate":
		if agent == nil {
			return fmt.Errorf("agent is required")
		}
		return upsertAgentGate(agent, rec, true)
	case "agent.append_instructions":
		if agent == nil {
			return fmt.Errorf("agent is required")
		}
		return appendAgentText(&agent.Instructions, rec)
	case "agent.append_task_prompt":
		if agent == nil {
			return fmt.Errorf("agent is required")
		}
		return appendTaskPrompt(agent, rec)
	default:
		return fmt.Errorf("unsupported recommendation action %q", action)
	}
}

func appendTaskPrompt(agent *domain.AgentDefinition, rec domain.RunEvalRecommendation) error {
	text := recommendationText(rec)
	if text == "" {
		return fmt.Errorf("append text is empty")
	}
	if agent.TaskPrompt == nil {
		agent.TaskPrompt = &domain.TaskPromptSpec{}
	}
	for _, item := range agent.TaskPrompt.Constraints {
		if item == text {
			return nil
		}
	}
	agent.TaskPrompt.Constraints = append(agent.TaskPrompt.Constraints, text)
	return nil
}

func recommendationText(rec domain.RunEvalRecommendation) string {
	if text := strings.TrimSpace(rec.Text); text != "" {
		return text
	}
	if rec.Set == nil {
		return ""
	}
	for _, key := range []string{"text", "instructionsDelta"} {
		if value, ok := stringFrom(rec.Set, key); ok {
			return value
		}
	}
	return ""
}

func appendAgentText(dst *string, rec domain.RunEvalRecommendation) error {
	text := recommendationText(rec)
	if text == "" {
		return fmt.Errorf("append text is empty")
	}
	if strings.Contains(*dst, text) {
		return nil
	}
	if strings.TrimSpace(*dst) == "" {
		*dst = text
		return nil
	}
	*dst = strings.TrimSpace(*dst) + "\n\n" + text
	return nil
}

func updateWorkflowNode(wf *domain.WorkflowDefinition, rec domain.RunEvalRecommendation) error {
	node, i, ok := findNode(wf, rec.NodeID)
	if !ok {
		return fmt.Errorf("node %q not found", rec.NodeID)
	}
	if rec.Set == nil {
		return fmt.Errorf("update_node requires set")
	}
	if v, ok := intFrom(rec.Set, "maxAttempts"); ok {
		if v < 1 {
			return fmt.Errorf("maxAttempts must be >= 1")
		}
		node.MaxAttempts = v
	}
	if v, ok := intFrom(rec.Set, "maxReworks"); ok {
		if v < 0 {
			return fmt.Errorf("maxReworks must be >= 0")
		}
		node.MaxReworks = v
	}
	if v, ok := stringFrom(rec.Set, "onFailure"); ok {
		if !allowedNodeFailure(v) {
			return fmt.Errorf("unsupported onFailure %q", v)
		}
		node.OnFailure = v
	}
	if v, ok := stringFrom(rec.Set, "retryStrategy"); ok {
		if !allowedRetry(v) {
			return fmt.Errorf("unsupported retryStrategy %q", v)
		}
		node.RetryStrategy = v
	}
	wf.Nodes[i] = node
	return nil
}

func upsertAgentGate(agent *domain.AgentDefinition, rec domain.RunEvalRecommendation, remove bool) error {
	gates, err := applyGateList(agent.Verification, rec, remove)
	if err != nil {
		return err
	}
	agent.Verification = gates
	return nil
}

func applyGateList(gates []domain.VerificationGate, rec domain.RunEvalRecommendation, remove bool) ([]domain.VerificationGate, error) {
	id := ""
	if rec.Set != nil {
		id, _ = stringFrom(rec.Set, "gateId")
	}
	if rec.Gate != nil && strings.TrimSpace(rec.Gate.ID) != "" {
		id = strings.TrimSpace(rec.Gate.ID)
	}
	if id == "" {
		return nil, fmt.Errorf("gate id is required")
	}
	if remove {
		out := gates[:0]
		found := false
		for _, gate := range gates {
			if gate.ID == id {
				found = true
				continue
			}
			out = append(out, gate)
		}
		if !found {
			return gates, fmt.Errorf("gate %q not found", id)
		}
		return out, nil
	}
	gate := domain.VerificationGate{ID: id, Mode: "command", Required: true}
	if rec.Gate != nil {
		gate = *rec.Gate
		gate.ID = id
	}
	if gate.Mode == "" {
		gate.Mode = "command"
	}
	if gate.OnFailure != "" && !allowedGateFailure(gate.OnFailure) {
		return nil, fmt.Errorf("unsupported gate onFailure %q", gate.OnFailure)
	}
	for i, existing := range gates {
		if existing.ID == id {
			gates[i] = gate
			return gates, nil
		}
	}
	return append(gates, gate), nil
}

func findNode(wf *domain.WorkflowDefinition, id string) (domain.WorkflowNode, int, bool) {
	id = strings.TrimSpace(id)
	for i, node := range wf.Nodes {
		if node.ID == id {
			return node, i, true
		}
	}
	return domain.WorkflowNode{}, -1, false
}

func intFrom(set map[string]any, key string) (int, bool) {
	raw, ok := set[key]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

func stringFrom(set map[string]any, key string) (string, bool) {
	raw, ok := set[key]
	if !ok {
		return "", false
	}
	value := strings.TrimSpace(fmt.Sprint(raw))
	if value == "" || value == "<nil>" {
		return "", false
	}
	return value, true
}

func allowedNodeFailure(v string) bool {
	return v == "fail" || v == "reject"
}

func allowedGateFailure(v string) bool {
	return v == "loop" || v == "reject" || v == "fail" || v == "human"
}

func allowedRetry(v string) bool {
	return v == "targeted_fix" || v == "full_redo" || v == ""
}

func CloneID(base, projectID string) string {
	base = strings.TrimSpace(base)
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return base + "-copy"
	}
	return base + "-" + projectID
}

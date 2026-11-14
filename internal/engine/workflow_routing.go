package engine

import (
	"agentflow/internal/domain"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func routeLabels(wf domain.WorkflowDefinition, nodeID string) []string {
	labels := map[string]bool{}
	for _, edge := range wf.Edges {
		if edge.Source == nodeID && edge.Condition != nil && edge.Condition.Type == "route" {
			for _, value := range edge.Condition.Values {
				labels[value] = true
			}
		}
	}
	values := make([]string, 0, len(labels))
	for value := range labels {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// missingRouteDecision identifies an inconsistent frozen graph without
// treating sources that are still executing or waiting for approval as errors.
func missingRouteDecision(wf domain.WorkflowDefinition, run domain.Run) string {
	for _, edge := range wf.Edges {
		if edge.Condition == nil {
			continue
		}
		if node := run.Nodes[edge.Source]; node != nil && node.Status == "succeeded" && node.RouteDecision == nil {
			return edge.Source
		}
	}
	return ""
}

func missingRouteDecisionMessage(nodeID string) string {
	return fmt.Sprintf("节点「%s」已成功但缺少路由决策，请从该节点重新运行", nodeID)
}

// Prefixed namespaces keep numeric IDs distinct from index fallbacks.
func routeEdgeKey(edge domain.WorkflowEdge, index int) string {
	if edge.ID != "" {
		return "id:" + edge.ID
	}
	return "index:" + strconv.Itoa(index)
}

func routeEdgeSelected(decision *domain.RouteDecision, edge domain.WorkflowEdge, index int) bool {
	if decision.EdgeKeyVersion == 0 {
		return decision.SelectedEdges[strconv.Itoa(index)]
	}
	return decision.SelectedEdges[routeEdgeKey(edge, index)]
}

func decideRoute(wf domain.WorkflowDefinition, node domain.WorkflowNode, route domain.RouteValues, artifacts []domain.Artifact) (*domain.RouteDecision, error) {
	labels := routeLabels(wf, node.ID)
	allowed := map[string]bool{}
	for _, label := range labels {
		allowed[label] = true
	}
	for _, value := range route {
		if len(labels) > 0 && !allowed[value] {
			return nil, fmt.Errorf("节点 %s 路由值无效：可选值 %v，实际提交 %v", node.ID, labels, route)
		}
	}
	decision := &domain.RouteDecision{Values: route, SelectedEdges: map[string]bool{}, EdgeKeyVersion: 1}
	hasConditions, matched := false, false
	for i, edge := range wf.Edges {
		if edge.Source != node.ID || edge.Condition == nil {
			continue
		}
		hasConditions = true
		condition := edge.Condition
		selected := false
		switch condition.Type {
		case "route":
			for _, value := range route {
				for _, option := range condition.Values {
					if value == option {
						selected = true
					}
				}
			}
		case "artifact":
			for _, artifact := range artifacts {
				if artifact.Name != condition.Artifact {
					continue
				}
				var value any
				if err := json.Unmarshal([]byte(artifact.Content), &value); err != nil {
					return nil, fmt.Errorf("产物 %s 不是有效 JSON: %w", condition.Artifact, err)
				}
				value, found := jsonPointer(value, condition.Pointer)
				var expected any
				if err := json.Unmarshal(condition.Equals, &expected); err != nil {
					return nil, fmt.Errorf("条件值无效: %w", err)
				}
				if found && jsonEqual(value, expected) {
					selected = true
				}
			}
		default:
			return nil, fmt.Errorf("不支持的条件类型 %q", condition.Type)
		}
		decision.SelectedEdges[routeEdgeKey(edge, i)] = selected
		matched = matched || selected
	}
	if !hasConditions {
		return nil, nil
	}
	if !matched && node.DefaultTarget != "" {
		for i, edge := range wf.Edges {
			if edge.Source == node.ID && edge.Target == node.DefaultTarget {
				decision.SelectedEdges[routeEdgeKey(edge, i)] = true
				matched = true
			}
		}
	}
	if !matched {
		return nil, fmt.Errorf("节点 %s 没有匹配的条件且没有默认分支；可选路由 %v，实际提交 %v", node.ID, labels, route)
	}
	return decision, nil
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func jsonPointer(value any, pointer string) (any, bool) {
	if pointer == "" {
		return value, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	for _, part := range strings.Split(pointer[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			value = v[i]
		default:
			return nil, false
		}
	}
	return value, true
}

// nodeReadiness waits for all incoming decisions before deciding whether to skip.
func nodeReadiness(wf domain.WorkflowDefinition, run domain.Run, node domain.WorkflowNode) (ready, skip bool, reason string) {
	incoming, selected, skipped := 0, 0, 0
	var reasons []string
	for i, edge := range wf.Edges {
		if edge.Target != node.ID {
			continue
		}
		incoming++
		up := run.Nodes[edge.Source]
		if up == nil {
			return false, false, ""
		}
		switch up.Status {
		case "skipped":
			skipped++
			reason := edge.Source + " 已跳过"
			if up.SkipReason != "" {
				reason += "（" + up.SkipReason + "）"
			}
			reasons = append(reasons, reason)
		case "succeeded":
			conditional := edge.Condition != nil
			for _, source := range wf.Nodes {
				if source.ID == edge.Source && source.DefaultTarget == edge.Target {
					conditional = true
					break
				}
			}
			if conditional {
				if up.RouteDecision == nil {
					return false, false, ""
				}
				if !routeEdgeSelected(up.RouteDecision, edge, i) {
					skipped++
					if edge.Condition != nil && edge.Condition.Type == "artifact" {
						reasons = append(reasons, fmt.Sprintf("%s 的产物 %s 字段 %s 未匹配条件 %s", edge.Source, edge.Condition.Artifact, edge.Condition.Pointer, edge.Condition.Equals))
					} else if edge.Condition == nil {
						reasons = append(reasons, fmt.Sprintf("%s 已命中其他条件，未选择默认分支", edge.Source))
					} else {
						reasons = append(reasons, fmt.Sprintf("%s 路由 %v 未选择此分支", edge.Source, up.RouteDecision.Values))
					}
					continue
				}
			}
			selected++
		default:
			return false, false, ""
		}
	}
	if incoming == 0 {
		return true, false, ""
	}
	if node.JoinRule == "none_failed_min_one_success" && selected > 0 {
		return true, false, ""
	}
	if skipped == 0 {
		return true, false, ""
	}
	return false, true, strings.Join(reasons, "；")
}

func validateJoinRule(node domain.WorkflowNode) error {
	if node.JoinRule != "" && node.JoinRule != "all_success" && node.JoinRule != "none_failed_min_one_success" {
		return fmt.Errorf("node %s has invalid joinRule %q", node.ID, node.JoinRule)
	}
	return nil
}

func validateRouting(wf domain.WorkflowDefinition) error {
	nodes := map[string]domain.WorkflowNode{}
	for _, node := range wf.Nodes {
		nodes[node.ID] = node
		if err := validateJoinRule(node); err != nil {
			return err
		}
	}
	// A source's routing decisions must identify each outgoing edge uniquely.
	routingSources := map[string]bool{}
	for _, edge := range wf.Edges {
		if edge.Condition != nil {
			routingSources[edge.Source] = true
		}
	}
	edgeIDs := map[string]map[string]bool{}
	for _, edge := range wf.Edges {
		if !routingSources[edge.Source] || edge.ID == "" {
			continue
		}
		if edgeIDs[edge.Source] == nil {
			edgeIDs[edge.Source] = map[string]bool{}
		}
		if edgeIDs[edge.Source][edge.ID] {
			return fmt.Errorf("node %s has duplicate routing edge id %q", edge.Source, edge.ID)
		}
		edgeIDs[edge.Source][edge.ID] = true
	}
	labels := map[string]map[string]bool{}
	conditions := map[string]bool{}
	defaults := map[string]bool{}
	for _, edge := range wf.Edges {
		source := nodes[edge.Source]
		if source.DefaultTarget != "" && source.DefaultTarget == edge.Target {
			if edge.Condition != nil {
				return fmt.Errorf("default edge %s must be unconditional", edge.ID)
			}
			defaults[edge.Source] = true
		}
		if edge.Condition == nil {
			continue
		}
		if edge.When != "" && edge.When != "success" {
			return fmt.Errorf("edge %s condition requires success", edge.ID)
		}
		conditions[edge.Source] = true
		if source.Type != "agent" && source.Type != "script" && source.Type != "command" {
			return fmt.Errorf("node %s cannot evaluate conditional edges", source.ID)
		}
		c := edge.Condition
		switch c.Type {
		case "route":
			if source.Type != "agent" && source.Type != "script" && source.Type != "command" {
				return fmt.Errorf("node %s cannot produce route labels", source.ID)
			}
			if source.Type != "agent" && source.ExecutionScope == domain.ExecutionScopeRepositories {
				return fmt.Errorf("node %s route is ambiguous for repository-scoped scripts", source.ID)
			}
			if len(c.Values) == 0 {
				return fmt.Errorf("edge %s has no route labels", edge.ID)
			}
			if labels[edge.Source] == nil {
				labels[edge.Source] = map[string]bool{}
			}
			for _, label := range c.Values {
				if strings.TrimSpace(label) == "" || labels[edge.Source][label] {
					return fmt.Errorf("node %s has empty or duplicate route label %q", edge.Source, label)
				}
				labels[edge.Source][label] = true
			}
		case "artifact":
			found := false
			for _, contract := range source.ArtifactOutputs {
				if contract.Name == c.Artifact {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("edge %s references undeclared artifact %q", edge.ID, c.Artifact)
			}
			if source.ExecutionScope == domain.ExecutionScopeRepositories {
				return fmt.Errorf("edge %s artifact condition is ambiguous for repository-scoped outputs", edge.ID)
			}
			if c.Pointer != "" && !strings.HasPrefix(c.Pointer, "/") {
				return fmt.Errorf("edge %s has invalid JSON pointer", edge.ID)
			}
			var v any
			if len(c.Equals) == 0 || json.Unmarshal(c.Equals, &v) != nil {
				return fmt.Errorf("edge %s has invalid comparison value", edge.ID)
			}
			switch v.(type) {
			case nil, bool, float64, string:
			default:
				return fmt.Errorf("edge %s comparison must be a JSON scalar", edge.ID)
			}
		default:
			return fmt.Errorf("edge %s has invalid condition type %q", edge.ID, c.Type)
		}
	}
	for _, node := range wf.Nodes {
		if node.DefaultTarget != "" && (!defaults[node.ID] || !conditions[node.ID]) {
			return fmt.Errorf("node %s has invalid defaultTarget", node.ID)
		}
	}
	return nil
}

// WorkflowRoutingWarnings points out joins that will themselves be skipped
// when one of their conditional branches is not selected.
func WorkflowRoutingWarnings(wf domain.WorkflowDefinition) []string {
	adj := map[string][]string{}
	incoming := map[string]int{}
	for _, edge := range wf.Edges {
		adj[edge.Source] = append(adj[edge.Source], edge.Target)
		incoming[edge.Target]++
	}
	branched := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		if branched[id] {
			return
		}
		branched[id] = true
		for _, next := range adj[id] {
			walk(next)
		}
	}
	for _, edge := range wf.Edges {
		if edge.Condition != nil {
			walk(edge.Target)
		}
	}
	var warnings []string
	for _, node := range wf.Nodes {
		if branched[node.ID] && incoming[node.ID] > 1 && (node.JoinRule == "" || node.JoinRule == "all_success") {
			warnings = append(warnings, fmt.Sprintf("节点 %s 是条件分支的汇合点，all_success 会在上游跳过时跳过此节点", node.ID))
		}
	}
	return warnings
}

// ValidateWorkflowRouting checks routing configuration at both save and run time.
func ValidateWorkflowRouting(wf domain.WorkflowDefinition) error { return validateRouting(wf) }

func propagateSkipped(wf domain.WorkflowDefinition, run *domain.Run) bool {
	changed := false
	for {
		pass := false
		for _, node := range wf.Nodes {
			nr := run.Nodes[node.ID]
			if nr == nil || nr.Status != "pending" {
				continue
			}
			_, skip, reason := nodeReadiness(wf, *run, node)
			if !skip {
				continue
			}
			nr.Status, nr.SkipReason, nr.FinishedAt = "skipped", reason, time.Now().UTC()
			pass, changed = true, true
		}
		if !pass {
			break
		}
	}
	return changed
}

package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"fmt"
	"path/filepath"
	"strings"
)

func readyNodes(wf domain.WorkflowDefinition, r domain.Run) []domain.WorkflowNode {
	out := []domain.WorkflowNode{}
	for _, n := range wf.Nodes {
		if r.Nodes[n.ID] == nil || r.Nodes[n.ID].Status != "pending" {
			continue
		}
		ready, _, _ := nodeReadiness(wf, r, n)
		if ready {
			out = append(out, n)
		}
	}
	return out
}

func latestWorkflowDefinition(st *repo.Store, id string) (domain.WorkflowDefinition, bool) {
	if st == nil || strings.TrimSpace(id) == "" {
		return domain.WorkflowDefinition{}, false
	}
	return st.Workflow(id)
}

func workflowForRun(s *repo.Store, run domain.Run) domain.WorkflowDefinition {
	if run.WorkflowSnapshot.ID != "" {
		// Compatibility: runs created before sandbox ownership moved to the
		// workflow may have a frozen snapshot without sandboxProfileId. Keep
		// the frozen graph and use the legacy default local profile so retries
		// and approvals do not depend on later workflow edits.
		return defaultWorkflowSandboxProfile(run.WorkflowSnapshot)
	}
	wf, _ := latestWorkflowDefinition(s, run.WorkflowID)
	return defaultWorkflowSandboxProfile(wf)
}

func defaultWorkflowSandboxProfile(wf domain.WorkflowDefinition) domain.WorkflowDefinition {
	if strings.TrimSpace(wf.SandboxProfileID) == "" {
		wf.SandboxProfileID = "local"
	}
	return wf
}

func ancestors(wf domain.WorkflowDefinition, start string) map[string]bool {
	incoming := map[string][]string{}
	for _, edge := range wf.Edges {
		incoming[edge.Target] = append(incoming[edge.Target], edge.Source)
	}
	out := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		for _, src := range incoming[id] {
			if !out[src] {
				out[src] = true
				walk(src)
			}
		}
	}
	walk(start)
	return out
}

func descendants(wf domain.WorkflowDefinition, start string) map[string]bool {
	out := map[string]bool{}
	changed := true
	for changed {
		changed = false
		for _, e := range wf.Edges {
			if e.Source == start || out[e.Source] {
				if !out[e.Target] {
					out[e.Target] = true
					changed = true
				}
			}
		}
	}
	return out
}

func replayUpstreamSucceeded(run domain.Run, nodeID string) bool {
	for _, n := range run.WorkflowSnapshot.Nodes {
		if n.ID == nodeID {
			ready, _, _ := nodeReadiness(run.WorkflowSnapshot, run, n)
			if !ready {
				return false
			}
			break
		}
	}
	seen := map[string]bool{nodeID: true}
	queue := []string{nodeID}
	for len(queue) > 0 {
		target := queue[0]
		queue = queue[1:]
		for _, edge := range run.WorkflowSnapshot.Edges {
			if edge.Target != target || seen[edge.Source] {
				continue
			}
			upstream := run.Nodes[edge.Source]
			if upstream == nil || (upstream.Status != "succeeded" && upstream.Status != "skipped") {
				return false
			}
			seen[edge.Source] = true
			queue = append(queue, edge.Source)
		}
	}
	return true
}

func allSucceeded(wf domain.WorkflowDefinition, r domain.Run) bool {
	successfulSink := false
	hasOutgoing := map[string]bool{}
	for _, edge := range wf.Edges {
		hasOutgoing[edge.Source] = true
	}
	for _, definition := range wf.Nodes {
		n := r.Nodes[definition.ID]
		if n == nil || (n.Status != "succeeded" && n.Status != "skipped") {
			return false
		}
		if n.Status == "succeeded" && !hasOutgoing[definition.ID] {
			successfulSink = true
		}
	}
	return len(wf.Nodes) == 0 || successfulSink
}

func allRoutingFinished(wf domain.WorkflowDefinition, r domain.Run) bool {
	if len(wf.Nodes) == 0 {
		return false
	}
	for _, node := range wf.Nodes {
		nr := r.Nodes[node.ID]
		if nr == nil || (nr.Status != "succeeded" && nr.Status != "skipped") {
			return false
		}
	}
	return true
}

func hasWaiting(wf domain.WorkflowDefinition, r domain.Run) bool {
	for _, definition := range wf.Nodes {
		if n := r.Nodes[definition.ID]; n != nil && n.Status == "waiting_approval" {
			return true
		}
	}
	return false
}

// cancelledNode returns a node stopped by a cancel that nothing will run
// again unless an operator resumes it.
func cancelledNode(wf domain.WorkflowDefinition, r domain.Run) (domain.WorkflowNode, bool) {
	for _, definition := range wf.Nodes {
		if n := r.Nodes[definition.ID]; n != nil && n.Status == "cancelled" {
			return definition, true
		}
	}
	return domain.WorkflowNode{}, false
}

func hasFailed(wf domain.WorkflowDefinition, r domain.Run) bool {
	for _, definition := range wf.Nodes {
		if n := r.Nodes[definition.ID]; n != nil && n.Status == "failed" {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func validate(wf domain.WorkflowDefinition) error {
	if err := domain.ValidateWorkflowPreparation(wf); err != nil {
		return err
	}
	if strings.TrimSpace(wf.SandboxProfileID) == "" {
		return fmt.Errorf("workflow sandbox profile is required")
	}
	ids := map[string]bool{}
	for _, n := range wf.Nodes {
		if n.ID == "" {
			return fmt.Errorf("node id is required")
		}
		if ids[n.ID] {
			return fmt.Errorf("duplicate node %s", n.ID)
		}
		ids[n.ID] = true
		switch n.Type {
		case "agent", "approval":
		case "sop":
			if strings.TrimSpace(n.SOPWorkflowID) == "" {
				return fmt.Errorf("node %s sopWorkflowId is required", n.ID)
			}
			if strings.TrimSpace(n.SOPWorkflowID) == strings.TrimSpace(wf.ID) {
				return fmt.Errorf("node %s cannot reference its own workflow", n.ID)
			}
		case "script", "command":
			language := n.ScriptLanguage
			script := n.Script
			if n.Type == "command" {
				language, script = "shell", n.Command
			}
			if language == "" {
				language = "shell"
			}
			if language != "shell" && language != "python" {
				return fmt.Errorf("node %s has invalid scriptLanguage %q", n.ID, language)
			}
			if strings.TrimSpace(script) == "" {
				return fmt.Errorf("node %s script is required", n.ID)
			}
		default:
			return fmt.Errorf("node %s has invalid type %q", n.ID, n.Type)
		}
		if n.OnFailure != "" && n.OnFailure != "fail" && n.OnFailure != "reject" && n.OnFailure != "human" && n.OnFailure != "loop" {
			return fmt.Errorf("node %s has invalid onFailure %q", n.ID, n.OnFailure)
		}
		if scope := strings.TrimSpace(n.ExecutionScope); scope != "" && scope != domain.ExecutionScopeRun && scope != domain.ExecutionScopeRepositories {
			return fmt.Errorf("node %s has invalid executionScope %q", n.ID, scope)
		}
		if n.RetryStrategy != "" && n.RetryStrategy != "targeted_fix" && n.RetryStrategy != "full_redo" {
			return fmt.Errorf("node %s has invalid retryStrategy %q", n.ID, n.RetryStrategy)
		}
		if n.JoinRule != "" && n.JoinRule != "all_success" && n.JoinRule != "none_failed_min_one_success" {
			return fmt.Errorf("node %s has invalid joinRule %q", n.ID, n.JoinRule)
		}
		artifactNames := map[string]bool{}
		for _, contract := range n.ArtifactOutputs {
			clean := filepath.Clean(contract.Path)
			if contract.Name == "" || clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
				return fmt.Errorf("node %s has invalid artifact contract %q", n.ID, contract.Name)
			}
			if artifactNames[contract.Name] {
				return fmt.Errorf("node %s has duplicate artifact %s", n.ID, contract.Name)
			}
			artifactNames[contract.Name] = true
		}
	}
	for _, n := range wf.Nodes {
		if n.RejectTo != "" && !ids[n.RejectTo] {
			return fmt.Errorf("node %s reject target not found", n.ID)
		}
		if n.OnFailure == "reject" && n.RejectTo == "" {
			return fmt.Errorf("node %s reject target is required", n.ID)
		}
	}
	indegree := map[string]int{}
	adj := map[string][]string{}
	for _, e := range wf.Edges {
		if !ids[e.Source] || !ids[e.Target] {
			return fmt.Errorf("edge references missing node")
		}
		indegree[e.Target]++
		adj[e.Source] = append(adj[e.Source], e.Target)
	}
	if err := validateRouting(wf); err != nil {
		return err
	}
	q := []string{}
	for id := range ids {
		if indegree[id] == 0 {
			q = append(q, id)
		}
	}
	seen := 0
	for len(q) > 0 {
		x := q[0]
		q = q[1:]
		seen++
		for _, v := range adj[x] {
			indegree[v]--
			if indegree[v] == 0 {
				q = append(q, v)
			}
		}
	}
	if seen != len(ids) {
		return fmt.Errorf("workflow must be a DAG; use rejectTo for controlled rework")
	}
	return nil
}

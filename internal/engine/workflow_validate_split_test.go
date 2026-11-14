package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestValidateNodeChecksOwnFields(t *testing.T) {
	ok := []domain.WorkflowNode{
		{ID: "a", Type: "agent"},
		{ID: "b", Type: "approval", OnFailure: "human", RetryStrategy: "full_redo"},
		{ID: "c", Type: "sop", SOPWorkflowID: "other"},
		{ID: "d", Type: "script", Script: "echo ok"},
		{ID: "e", Type: "command", Command: "echo ok", ScriptLanguage: "ruby"},
		{ID: "f", Type: "agent", ExecutionScope: domain.ExecutionScopeRepositories, ArtifactOutputs: []domain.ArtifactContract{{Name: "r", Path: "out/r.md"}}},
		{ID: "g", Type: "agent", JoinRule: "all_success"},
		{ID: "h", Type: "agent", JoinRule: "none_failed_min_one_success"},
	}
	for _, n := range ok {
		if err := validateNode("wf", n); err != nil {
			t.Fatalf("node %s rejected: %v", n.ID, err)
		}
	}
	bad := []struct {
		node domain.WorkflowNode
		want string
	}{
		{domain.WorkflowNode{ID: "a", Type: "sop", SOPWorkflowID: " "}, "node a sopWorkflowId is required"},
		{domain.WorkflowNode{ID: "a", Type: "sop", SOPWorkflowID: " wf "}, "node a cannot reference its own workflow"},
		{domain.WorkflowNode{ID: "a", Type: "script", ScriptLanguage: "ruby", Script: "x"}, `node a has invalid scriptLanguage "ruby"`},
		{domain.WorkflowNode{ID: "a", Type: "command"}, "node a script is required"},
		{domain.WorkflowNode{ID: "a", Type: "bad"}, `node a has invalid type "bad"`},
		{domain.WorkflowNode{ID: "a", Type: "agent", OnFailure: "bad"}, `node a has invalid onFailure "bad"`},
		{domain.WorkflowNode{ID: "a", Type: "agent", ExecutionScope: " bad "}, `node a has invalid executionScope "bad"`},
		{domain.WorkflowNode{ID: "a", Type: "agent", RetryStrategy: "bad"}, `node a has invalid retryStrategy "bad"`},
		{domain.WorkflowNode{ID: "a", Type: "agent", JoinRule: "bad"}, `node a has invalid joinRule "bad"`},
		{domain.WorkflowNode{ID: "a", Type: "agent", ArtifactOutputs: []domain.ArtifactContract{{Name: "x", Path: "/abs"}}}, `node a has invalid artifact contract "x"`},
		{domain.WorkflowNode{ID: "a", Type: "agent", ArtifactOutputs: []domain.ArtifactContract{{Name: "x", Path: "a"}, {Name: "x", Path: "b"}}}, "node a has duplicate artifact x"},
	}
	for i, tc := range bad {
		err := validateNode("wf", tc.node)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("case %d: err = %v, want %q", i, err, tc.want)
		}
	}
}

func TestValidateDAGRejectsMissingNodesAndCycles(t *testing.T) {
	ids := map[string]bool{"a": true, "b": true, "c": true}
	if err := validateDAG(ids, nil); err != nil {
		t.Fatalf("edgeless graph rejected: %v", err)
	}
	if err := validateDAG(ids, []domain.WorkflowEdge{{Source: "a", Target: "b"}, {Source: "a", Target: "c"}, {Source: "b", Target: "c"}}); err != nil {
		t.Fatalf("diamond DAG rejected: %v", err)
	}
	if err := validateDAG(ids, []domain.WorkflowEdge{{Source: "a", Target: "missing"}}); err == nil || err.Error() != "edge references missing node" {
		t.Fatalf("missing target err = %v", err)
	}
	cycle := []domain.WorkflowEdge{{Source: "a", Target: "b"}, {Source: "b", Target: "c"}, {Source: "c", Target: "b"}}
	if err := validateDAG(ids, cycle); err == nil || !strings.Contains(err.Error(), "workflow must be a DAG") {
		t.Fatalf("cycle err = %v", err)
	}
}

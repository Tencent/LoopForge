package engine

import (
	"testing"

	"agentflow/internal/domain"
)

// A failed branch must not settle the run while a sibling node is still
// running or waiting for input.
func TestSettleStatusForCompletedGraphWaitsForActiveNodes(t *testing.T) {
	wf := domain.WorkflowDefinition{ID: "wf", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "left", Type: "script"}, {ID: "right", Type: "agent"}}}
	for _, active := range []string{"running", "pending_input"} {
		run := domain.Run{ID: "settle-" + active, Status: "running", WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{
			"left":  {NodeID: "left", Status: "failed"},
			"right": {NodeID: "right", Status: active},
		}}
		if status, _, _, ok := settleStatusForCompletedGraph(wf, run); ok {
			t.Fatalf("%s sibling: graph settled as %q while a node is active", active, status)
		}
		run.Nodes["right"].Status = "succeeded"
		if status, _, _, ok := settleStatusForCompletedGraph(wf, run); !ok || status != "failed" {
			t.Fatalf("%s sibling: finished graph = %q %v, want failed", active, status, ok)
		}
	}
	extra := domain.Run{ID: "settle-extra", Status: "running", WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{
		"left":  {NodeID: "left", Status: "succeeded"},
		"right": {NodeID: "right", Status: "succeeded"},
		"retry": {NodeID: "retry", Status: "running"},
	}}
	if _, _, _, ok := settleStatusForCompletedGraph(wf, extra); ok {
		t.Fatal("graph settled while a node outside the snapshot is still running")
	}
}

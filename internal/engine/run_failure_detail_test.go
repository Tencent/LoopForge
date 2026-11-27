package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// TestScriptNodeFailureKeepsScriptOutput covers issue #1191: a failing gate
// script must keep its stderr in the node error instead of only "exit status 1".
func TestScriptNodeFailureKeepsScriptOutput(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := leaseEngine(t, st, "script-failure-detail-executor")

	node := domain.WorkflowNode{
		ID: "gate", Name: "Gate", Type: "script", ScriptLanguage: "shell", MaxAttempts: 1,
		Script: `printf "INVALID_CHALLENGE_EVIDENCE: DEL-05 / 缺有效路径或观察事实\n" >&2; exit 1`,
	}
	wf := domain.WorkflowDefinition{ID: "wf-script-failure", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{node}}
	run := domain.Run{
		ID: "run-script-failure-detail", Status: "running", WorkflowID: wf.ID, WorkflowSnapshot: wf,
		Nodes: map[string]*domain.NodeRun{node.ID: {NodeID: node.ID, Status: "pending"}},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.acquireLease(run); err != nil {
		t.Fatal(err)
	}
	e.executeNode(context.Background(), run.ID, wf, node)
	nr := waitNodeTerminal(t, st, run.ID, node.ID)
	if nr.Status != "failed" {
		t.Fatalf("node = %+v, want failed", nr)
	}
	if !strings.Contains(nr.Error, "INVALID_CHALLENGE_EVIDENCE: DEL-05") {
		t.Fatalf("node error lost the script stderr: %q", nr.Error)
	}
	if !strings.Contains(nr.Error, "exit status 1") {
		t.Fatalf("node error lost the exit status: %q", nr.Error)
	}
}

// TestScriptNodeFailureOutputIsCapped keeps a noisy script from bloating node
// errors while preserving the tail, where gate scripts usually print the cause.
func TestScriptNodeFailureOutputIsCapped(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := leaseEngine(t, st, "script-failure-cap-executor")

	node := domain.WorkflowNode{
		ID: "noisy", Name: "Noisy", Type: "script", ScriptLanguage: "shell", MaxAttempts: 1,
		Script: `head -c 6000 /dev/zero | tr '\0' 'x' >&2; printf "\nTAIL-MARKER\n" >&2; exit 2`,
	}
	wf := domain.WorkflowDefinition{ID: "wf-script-cap", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{node}}
	run := domain.Run{
		ID: "run-script-failure-cap", Status: "running", WorkflowID: wf.ID, WorkflowSnapshot: wf,
		Nodes: map[string]*domain.NodeRun{node.ID: {NodeID: node.ID, Status: "pending"}},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.acquireLease(run); err != nil {
		t.Fatal(err)
	}
	e.executeNode(context.Background(), run.ID, wf, node)
	nr := waitNodeTerminal(t, st, run.ID, node.ID)
	if nr.Status != "failed" {
		t.Fatalf("node = %+v, want failed", nr)
	}
	if len(nr.Error) > maxScriptFailureDetail+1024 {
		t.Fatalf("node error is not capped: %d bytes", len(nr.Error))
	}
	if !strings.Contains(nr.Error, "TAIL-MARKER") {
		t.Fatalf("node error dropped the output tail: %q", nr.Error[:min(200, len(nr.Error))])
	}
}

// waitNodeTerminal waits for a node to reach a terminal state: workspace
// preparation may finish on a background worker before the node outcome lands.
func waitNodeTerminal(t *testing.T, st *repo.Store, runID, nodeID string) *domain.NodeRun {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got, ok := st.RunState(runID)
		if ok && got.Nodes[nodeID] != nil {
			nr := got.Nodes[nodeID]
			if nr.Status == "failed" || nr.Status == "succeeded" {
				return nr
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %s did not reach a terminal state", nodeID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

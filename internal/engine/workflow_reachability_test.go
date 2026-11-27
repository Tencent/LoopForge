package engine

import (
	"fmt"
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestWorkflowReachabilityAcrossBranches(t *testing.T) {
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{
		{Source: "b", Target: "d"}, {Source: "c", Target: "d"},
		{Source: "a", Target: "b"}, {Source: "a", Target: "c"}, {Source: "a", Target: "b"},
		{Source: "other", Target: "isolated"},
	}}
	if got, want := descendants(wf, "a"), map[string]bool{"b": true, "c": true, "d": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("descendants=%v, want %v", got, want)
	}
	run := domain.Run{WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{}}
	for _, id := range []string{"a", "b", "c", "d"} {
		run.Nodes[id] = &domain.NodeRun{Status: "succeeded"}
	}
	if !replayUpstreamSucceeded(run, "d") {
		t.Fatal("all ancestors succeeded")
	}
	run.Nodes["b"].Status = "failed"
	if replayUpstreamSucceeded(run, "d") {
		t.Fatal("accepted failed branch")
	}
	delete(run.Nodes, "b")
	if replayUpstreamSucceeded(run, "d") {
		t.Fatal("accepted missing branch")
	}
	// Persisted legacy graphs can contain a cycle; traversal must still stop.
	wf.Edges = append(wf.Edges, domain.WorkflowEdge{Source: "d", Target: "a"})
	if got, want := descendants(wf, "a"), map[string]bool{"a": true, "b": true, "c": true, "d": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cyclic descendants=%v", got)
	}
}

func benchmarkWorkflowChain() domain.Run {
	const count = 2000
	run := domain.Run{Nodes: map[string]*domain.NodeRun{}}
	for i := count - 1; i >= 0; i-- {
		id := fmt.Sprint(i)
		run.Nodes[id] = &domain.NodeRun{Status: "succeeded"}
		if i > 0 {
			run.WorkflowSnapshot.Edges = append(run.WorkflowSnapshot.Edges, domain.WorkflowEdge{Source: fmt.Sprint(i - 1), Target: id})
		}
	}
	return run
}

func BenchmarkWorkflowDescendantsReverseChain(b *testing.B) {
	run := benchmarkWorkflowChain()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		descendants(run.WorkflowSnapshot, "0")
	}
}

func BenchmarkWorkflowReplayAncestors(b *testing.B) {
	run := benchmarkWorkflowChain()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		replayUpstreamSucceeded(run, "1999")
	}
}

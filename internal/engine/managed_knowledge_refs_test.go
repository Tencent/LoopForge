package engine

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// recordingManagedAgent reports whether the turn reached the provider and what
// prompt it carried, so tests can prove a rejected node never dispatches.
type recordingManagedAgent struct {
	runs    int
	prompts []string
}

func (a *recordingManagedAgent) Run(_ context.Context, prompt string, onText func(string)) (string, error) {
	a.runs++
	a.prompts = append(a.prompts, prompt)
	onText("managed-answer")
	return "managed-answer", nil
}

func TestManagedNodeKnowledgeRefsNotSilentlyDropped(t *testing.T) {
	refs := []domain.KnowledgeRef{{ConnectorID: "kc-rv1469", Mode: "all"}}
	cases := []struct {
		name     string
		provider string
		node     domain.WorkflowNode
		runRefs  *[]domain.KnowledgeRef
		wantErr  []string
	}{
		{
			name:     "managed node with an existing connector binding is rejected",
			provider: domain.A2AExecutorID,
			node:     domain.WorkflowNode{ID: "n1", Name: "A2A", Type: "agent", KnowledgeRefs: refs},
			wantErr:  []string{"知识库", "kc-rv1469", "托管 Agent"},
		},
		{
			name:     "run-level pin is rejected on a managed node",
			provider: domain.WMAExecutorID,
			node:     domain.WorkflowNode{ID: "n1", Name: "WMA", Type: "agent"},
			runRefs:  &[]domain.KnowledgeRef{{ConnectorID: "kc-run", Mode: "all"}},
			wantErr:  []string{"知识库", "kc-run", "托管 Agent"},
		},
		{
			name:     "explicit empty knowledgeRefs override is not a binding",
			provider: domain.A2AExecutorID,
			node:     domain.WorkflowNode{ID: "n1", Name: "A2A", Type: "agent"},
			runRefs:  &[]domain.KnowledgeRef{},
		},
		{
			name:     "managed node without knowledge bindings stays allowed",
			provider: domain.WMAExecutorID,
			node:     domain.WorkflowNode{ID: "n1", Name: "WMA", Type: "agent"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := managedNodeKnowledgeUnsupported(tc.node, tc.provider, tc.runRefs)
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("managedNodeKnowledgeUnsupported() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("managedNodeKnowledgeUnsupported() = nil, want an explicit rejection")
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

// TestReview1469ManagedNodeKnowledgeRefs mirrors the reviewer's repro: the same
// connector reference bound to a managed node must fail loudly instead of being
// dropped, while a managed node without knowledge bindings still runs.
func TestReview1469ManagedNodeKnowledgeRefs(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	role := domain.AgentDefinition{
		ID: "rv1469-a2a", Name: "A2A role", ProjectID: "default",
		ExecutorID: domain.A2AExecutorID, ExternalAgentTarget: domain.ExternalAgentRemote,
		Prompt: "answer the question",
	}
	if err := store.PutAgent(role); err != nil {
		t.Fatal(err)
	}
	agent := &recordingManagedAgent{}
	eng := New(store, runtime.NewManagerWithLimit(t.TempDir(), 1))
	if err := eng.ConfigureManagedAgent(domain.A2AExecutorID, "default", 42, agent); err != nil {
		t.Fatal(err)
	}

	execute := func(runID string, node domain.WorkflowNode, runRefs *[]domain.KnowledgeRef) (domain.Run, string) {
		t.Helper()
		wf := domain.WorkflowDefinition{
			ID: "wf-rv1469-" + runID, Name: "wf", ProjectID: "default",
			SandboxProfileID: "test-native",
			Nodes:            []domain.WorkflowNode{node},
		}
		run := domain.Run{
			ID: runID, Status: "running", WorkflowID: wf.ID, WorkflowSnapshot: wf,
			ExecutionContext: domain.TelemetryContext{ProjectID: "default", ResponsibleUserID: 42, KnowledgeRefs: runRefs},
			Nodes:            map[string]*domain.NodeRun{node.ID: {NodeID: node.ID, Status: "pending"}},
		}
		if err := store.PutRun(run); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.acquireLease(run); err != nil {
			t.Fatal(err)
		}
		eng.executeNode(context.Background(), run.ID, wf, node)
		got, ok := store.Run(run.ID)
		if !ok {
			t.Fatalf("run %s not found", run.ID)
		}
		return got, got.Nodes[node.ID].Error
	}

	managedWithKB := domain.WorkflowNode{
		ID: "n1", Name: "Managed with KB", Type: "agent", AgentID: role.ID,
		KnowledgeRefs: []domain.KnowledgeRef{{ConnectorID: "kc-rv1469", Mode: "all"}},
	}
	got, nodeErr := execute("rv1469-managed-existing-connector", managedWithKB, nil)
	if got.Nodes["n1"].Status != "failed" {
		t.Fatalf("managed node with knowledge refs status = %q, want failed (error=%q)", got.Nodes["n1"].Status, nodeErr)
	}
	if !strings.Contains(nodeErr, "kc-rv1469") || !strings.Contains(nodeErr, "知识库") {
		t.Fatalf("node error %q should name the dropped connector and the knowledge binding", nodeErr)
	}
	if agent.runs != 0 {
		t.Fatalf("provider was called %d times for a node whose knowledge binding cannot be honoured", agent.runs)
	}
	if len(got.Artifacts) != 0 {
		t.Fatalf("rejected node produced artifacts: %+v", got.Artifacts)
	}

	got, nodeErr = execute("rv1469-managed-run-level-pin", domain.WorkflowNode{ID: "n1", Name: "Managed pinned", Type: "agent", AgentID: role.ID},
		&[]domain.KnowledgeRef{{ConnectorID: "kc-run", Mode: "all"}})
	if got.Nodes["n1"].Status != "failed" || !strings.Contains(nodeErr, "kc-run") {
		t.Fatalf("run-level pin on managed node: status=%q error=%q, want failed naming kc-run", got.Nodes["n1"].Status, nodeErr)
	}
	if agent.runs != 0 {
		t.Fatalf("provider was called %d times for a run-level pin the managed node cannot honour", agent.runs)
	}

	got, nodeErr = execute("rv1469-managed-no-knowledge", domain.WorkflowNode{ID: "n1", Name: "Managed plain", Type: "agent", AgentID: role.ID}, nil)
	if got.Nodes["n1"].Status != "succeeded" || nodeErr != "" {
		t.Fatalf("managed node without knowledge bindings: status=%q error=%q, want succeeded", got.Nodes["n1"].Status, nodeErr)
	}
	if agent.runs != 1 || !strings.Contains(agent.prompts[0], "answer the question") {
		t.Fatalf("managed node without knowledge bindings should dispatch once, runs=%d prompts=%q", agent.runs, agent.prompts)
	}
}

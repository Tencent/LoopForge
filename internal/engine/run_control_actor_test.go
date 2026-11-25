package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// controlActorFixture is one run parked at a human approval gate.
func controlActorFixture(t *testing.T) (*repo.Store, *Engine, string) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	wf := domain.WorkflowDefinition{
		ID: "control-actor", Name: "control actor", SandboxProfileID: "test-native",
		Nodes: []domain.WorkflowNode{{ID: "gate", Name: "Gate", Type: "approval"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID:               "run-control-actor",
		WorkflowID:       wf.ID,
		WorkflowSnapshot: wf,
		Status:           "waiting",
		Nodes: map[string]*domain.NodeRun{
			"gate": {NodeID: "gate", Status: "waiting_approval", Attempt: 1},
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(t.TempDir()))
	return s, e, run.ID
}

// TestApproveRecordsActors asserts every human decision writes the acting
// identity where an audit reader finds it: the node record, the control
// telemetry attributes and the decision artifact.
func TestApproveRecordsActors(t *testing.T) {
	s, e, runID := controlActorFixture(t)
	actor := domain.Actor{ID: 7, Name: "zhang"}
	if err := e.Approve(actor, runID, "gate", "通过，改动符合验收标准"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	run, ok := s.Run(runID)
	if !ok {
		t.Fatal("run gone")
	}
	n := run.Nodes["gate"]
	if n == nil || n.Status != "succeeded" {
		t.Fatalf("node = %+v", n)
	}
	if n.DecisionBy != "zhang" || n.DecisionByID != 7 {
		t.Fatalf("decision identity = %q/%d", n.DecisionBy, n.DecisionByID)
	}
	if !decisionArtifactMentions(run, "zhang") {
		t.Fatalf("decision artifact missing actor: %+v", run.Artifacts)
	}
	var found bool
	for _, ev := range run.Telemetry {
		if ev.Name != "gate.approved" {
			continue
		}
		found = true
		if ev.Attributes["actorName"] != "zhang" || ev.Attributes["actorIsPerson"] != true {
			t.Fatalf("telemetry attrs = %+v", ev.Attributes)
		}
	}
	if !found {
		t.Fatal("gate.approved telemetry missing")
	}
}

// TestApproveRecordsSystemActorWithoutIdentity verifies a zero actor is
// recorded as system-initiated rather than fabricating a person.
func TestApproveRecordsSystemActorWithoutIdentity(t *testing.T) {
	s, e, runID := controlActorFixture(t)
	if err := e.Approve(domain.Actor{}, runID, "gate", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	run, _ := s.Run(runID)
	n := run.Nodes["gate"]
	if n.DecisionBy != "" || n.DecisionByID != 0 {
		t.Fatalf("system decision should keep zero identity: %q/%d", n.DecisionBy, n.DecisionByID)
	}
	if !decisionArtifactMentions(run, "system") {
		t.Fatalf("decision artifact should mention system: %+v", run.Artifacts)
	}
}

func decisionArtifactMentions(run domain.Run, actorLabel string) bool {
	for _, a := range run.Artifacts {
		if a.Name == "decision" && strings.Contains(a.Content, actorLabel) {
			return true
		}
	}
	return false
}

// TestRejectRecordsActors mirrors the approve assertions on the reject path.
func TestRejectRecordsActors(t *testing.T) {
	s, e, runID := controlActorFixture(t)
	wf := domain.WorkflowDefinition{
		ID: "control-actor-reject", Name: "reject", SandboxProfileID: "test-native",
		Nodes: []domain.WorkflowNode{
			{ID: "fix", Name: "Fix", Type: "script"},
			{ID: "gate", Name: "Gate", Type: "approval", RejectTo: "fix"},
		},
		Edges: []domain.WorkflowEdge{{ID: "fix-gate", Source: "fix", Target: "gate"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, ok := s.RunState(runID)
	if !ok {
		t.Fatal("run gone")
	}
	run.WorkflowSnapshot = wf
	run.Nodes["fix"] = &domain.NodeRun{NodeID: "fix", Status: "succeeded"}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	actor := domain.Actor{ID: 9, Name: "li"}
	if err := e.Reject(actor, runID, domain.RejectRequest{NodeID: "gate", TargetID: "fix", Reason: "测试不通过"}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	run, _ = s.Run(runID)
	if n := run.Nodes["gate"]; n == nil || n.DecisionBy != "li" || n.DecisionByID != 9 {
		t.Fatalf("gate decision identity = %+v", n)
	}
}

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

// pinnedApproverFixture parks one run at a gate whose workflow pins the node
// to approver 7.
func pinnedApproverFixture(t *testing.T) (*repo.Store, *Engine, string) {
	t.Helper()
	s, e, runID := controlActorFixture(t)
	run, ok := s.RunState(runID)
	if !ok {
		t.Fatal("run gone")
	}
	run.WorkflowSnapshot.Nodes[0].Approvers = []int64{7}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return s, e, runID
}

// TestPinnedApproversBlockOutsiders asserts an unlisted person cannot decide
// a node pinned to other approvers, on every decision entry point.
func TestPinnedApproversBlockOutsiders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action func(e *Engine) error
	}{
		{"approve", func(e *Engine) error {
			return e.Approve(domain.Actor{ID: 8, Name: "outsider"}, "run-control-actor", "gate", "")
		}},
		{"decline", func(e *Engine) error {
			return e.DeclineRetry(domain.Actor{ID: 8, Name: "outsider"}, "run-control-actor", "gate")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e, runID := pinnedApproverFixture(t)
			if err := tc.action(e); err == nil {
				t.Fatal("outsider decision should be refused")
			} else if !strings.Contains(err.Error(), "审批人") {
				t.Fatalf("unexpected error: %v", err)
			}
			run, _ := s.Run(runID)
			if n := run.Nodes["gate"]; n == nil || n.Status != "waiting_approval" {
				t.Fatalf("refused decision must not mutate the node: %+v", n)
			}
		})
	}
}

// TestPinnedApproversAllowListedAndAdmin asserts the listed user and a
// platform administrator can still decide a pinned node, and that a pinned
// gate without an identity bypass (system actor) is not blocked.
func TestPinnedApproversAllowListedAndAdmin(t *testing.T) {
	_, e, runID := pinnedApproverFixture(t)
	if err := e.Approve(domain.Actor{ID: 7, Name: "zhang"}, runID, "gate", ""); err != nil {
		t.Fatalf("listed approver: %v", err)
	}

	_, e2, run2 := pinnedApproverFixture(t)
	if err := e2.Approve(domain.Actor{ID: 5, Name: "root", Admin: true}, run2, "gate", ""); err != nil {
		t.Fatalf("platform admin: %v", err)
	}

	_, e3, run3 := pinnedApproverFixture(t)
	if err := e3.Approve(domain.Actor{Name: "auto"}, run3, "gate", ""); err != nil {
		t.Fatalf("automation actor should not be gated: %v", err)
	}
}

// TestUnpinnedGatesStayOpenToEveryone keeps the historical behavior: nodes
// without configured approvers accept decisions from any authenticated user.
func TestUnpinnedGatesStayOpenToEveryone(t *testing.T) {
	_, e, runID := controlActorFixture(t)
	if err := e.Approve(domain.Actor{ID: 8, Name: "anyone"}, runID, "gate", ""); err != nil {
		t.Fatalf("unpinned gate: %v", err)
	}
}

package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func newSquadTestRun() domain.Run {
	members := []domain.SquadMember{{ID: "writer", Name: "Writer"}, {ID: "reviewer", Name: "Reviewer"}, {ID: "analyst", Name: "Analyst"}}
	first := squadTurn(1, members[0])
	return domain.Run{ID: "squad-test", WorkflowID: "agent-squad", Status: "running", WorkflowSnapshot: domain.WorkflowDefinition{
		ID: "agent-squad", Squad: &domain.AgentSquad{Members: members, MaxTurns: 3}, Nodes: []domain.WorkflowNode{first},
	}, Nodes: map[string]*domain.NodeRun{first.ID: {NodeID: first.ID, Status: "running"}}}
}

func TestSquadRoutesFromResultsAndReplaysFromPrefix(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := &Engine{repo: s}
	r := newSquadTestRun()
	first := r.WorkflowSnapshot.Nodes[0]
	if err := e.commitSquadDecision(&r, first, `Work is ready.
LOOPFORGE_SQUAD {"action":"handoff","to":"analyst","summary":"Check receipt 317"}`); err != nil {
		t.Fatal(err)
	}
	second := r.WorkflowSnapshot.Nodes[1]
	if second.AgentID != "analyst" || len(r.WorkflowSnapshot.Nodes) != 2 {
		t.Fatalf("unexpected route: %#v", r.WorkflowSnapshot.Nodes)
	}
	r.Nodes[first.ID].Status = "waiting_approval"
	if ready := readyNodes(r.WorkflowSnapshot, r); len(ready) != 0 {
		t.Fatalf("dispatched before manual approval: %v", ready)
	}
	r.Nodes[first.ID].Status = "succeeded"
	if ready := readyNodes(r.WorkflowSnapshot, r); len(ready) != 1 || ready[0].ID != second.ID {
		t.Fatalf("ready: %v", ready)
	}
	if prompt := squadTurnPrompt(r, second); !strings.Contains(prompt, "receipt 317") || !strings.Contains(prompt, "reviewer") {
		t.Fatalf("missing handoff or roster in prompt: %v", prompt)
	}
	if err := e.commitSquadDecision(&r, second, `LOOPFORGE_SQUAD {"action":"handoff","to":"writer","summary":"Revise the total"}`); err != nil {
		t.Fatal(err)
	}
	r.Nodes[second.ID].Status = "succeeded"
	third := r.WorkflowSnapshot.Nodes[2]
	if third.AgentID != "writer" || third.ID == first.ID {
		t.Fatalf("revisit reused a turn: %#v", third)
	}
	if err := e.commitSquadDecision(&r, third, `LOOPFORGE_SQUAD {"action":"finish","summary":"Receipt fixed"}`); err != nil {
		t.Fatal(err)
	}
	r.Nodes[third.ID].Status = "succeeded"
	if !allSucceeded(r.WorkflowSnapshot, r) || len(r.Events) != 3 {
		t.Fatalf("incomplete run: %v", r.Events)
	}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	r, _ = s.RunProgress(r.ID)
	rewindSquad(&r, second.ID)
	r.Nodes[second.ID].Status = "pending"
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	r, _ = s.RunProgress(r.ID)
	if len(r.Nodes) != 2 || len(r.WorkflowSnapshot.Edges) != 1 || r.Nodes[second.ID].SquadDecision != nil || r.Nodes[first.ID].SquadDecision.To != "analyst" {
		t.Fatalf("rewind did not retain just the prefix: %#v", r)
	}
	if err := e.commitSquadDecision(&r, second, `LOOPFORGE_SQUAD {"action":"handoff","to":"reviewer","summary":"Independently verify"}`); err != nil {
		t.Fatal(err)
	}
	newThird := r.WorkflowSnapshot.Nodes[2]
	if newThird.ID == third.ID || newThird.AgentID != "reviewer" {
		t.Fatalf("replay did not get a fresh turn: %#v", newThird)
	}
}

func TestSquadStopsWithoutDispatchingInvalidOrExhaustedTurns(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		budget       int
	}{
		{"missing", "done", 6},
		{"trailing prose", "LOOPFORGE_SQUAD {\"action\":\"finish\",\"summary\":\"done\"}\nactually pending", 6},
		{"foreign role", `LOOPFORGE_SQUAD {"action":"handoff","to":"foreign","summary":"inspect"}`, 6},
		{"self", `LOOPFORGE_SQUAD {"action":"handoff","to":"writer","summary":"again"}`, 6},
		{"unknown action", `LOOPFORGE_SQUAD {"action":"run","summary":"no"}`, 6},
		{"ambiguous finish", `LOOPFORGE_SQUAD {"action":"finish","to":"writer","summary":"done"}`, 6},
		{"budget", `LOOPFORGE_SQUAD {"action":"handoff","to":"reviewer","summary":"more needed"}`, 1},
		{"empty summary", `LOOPFORGE_SQUAD {"action":"finish","summary":" "}`, 6},
		{"blocked", `LOOPFORGE_SQUAD {"action":"blocked","summary":"Need the receipt"}`, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSquadTestRun()
			r.WorkflowSnapshot.Squad.MaxTurns = tc.budget
			err := (&Engine{}).commitSquadDecision(&r, r.WorkflowSnapshot.Nodes[0], tc.output)
			if err == nil || len(r.Nodes) != 1 || len(r.WorkflowSnapshot.Edges) != 0 {
				t.Fatalf("error=%v nodes=%d", err, len(r.Nodes))
			}
		})
	}
	r := newSquadTestRun()
	r.WorkflowSnapshot.Squad.MaxTurns = 1
	if err := (&Engine{}).commitSquadDecision(&r, r.WorkflowSnapshot.Nodes[0], `LOOPFORGE_SQUAD {"action":"finish","summary":"One turn is enough"}`); err != nil {
		t.Fatal(err)
	}
}

func TestSquadApproveBeforeHandoffReturnsToSpeaker(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := &Engine{repo: s}
	r := newSquadTestRun()
	first := r.WorkflowSnapshot.Nodes[0]
	n := r.Nodes[first.ID]
	r.Status, n.Status, n.Activity = "waiting", "waiting_approval", "等待人工确认"
	n.GateResult = &domain.GateResult{Decision: "human", ActionRequired: "Which receipt?"}
	n.SessionID = "previous-session"
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := e.Approve(r.ID, first.ID, "Use receipt 317"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.RunProgress(r.ID)
	n = r.Nodes[first.ID]
	ready := readyNodes(r.WorkflowSnapshot, r)
	if r.Status != "running" || len(ready) != 1 || ready[0].ID != first.ID || n.Status != "pending" || n.SquadDecision != nil || !strings.Contains(n.Feedback, "Use receipt 317") || n.ResumeSessionID != "previous-session" {
		t.Fatalf("approval lost speaker or human context: %#v", n)
	}
}

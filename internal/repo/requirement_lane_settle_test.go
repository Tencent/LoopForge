package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func settleTestWorkflows() map[string]domain.WorkflowDefinition {
	return map[string]domain.WorkflowDefinition{
		"wf-a": {ID: "wf-a", Name: "流程 A", Nodes: []domain.WorkflowNode{
			{ID: "plan", Name: "规划"},
			{ID: "review", Name: "评审"},
		}},
	}
}

func TestSettledRequirementLaneFromRunningRun(t *testing.T) {
	workflows := settleTestWorkflows()
	run := domain.Run{ID: "run-1", WorkflowID: "wf-a", Status: "running", Nodes: map[string]*domain.NodeRun{
		"plan": {Status: "running"},
	}}
	lane := settledRequirementLane(domain.RequirementLane{WorkflowID: "wf-a"}, run, workflows, nil, time.Now())
	if lane.Status != "active" {
		t.Fatalf("status = %q, want active", lane.Status)
	}
	if len(lane.Stages) != 2 || lane.Stages[0].Status != "running" || lane.Stages[1].Status != "pending" {
		t.Fatalf("stages = %#v, want run node stages", lane.Stages)
	}
}

func TestSettledRequirementLaneActiveRunWaitingForHuman(t *testing.T) {
	workflows := settleTestWorkflows()
	// The parent run is active; only its nested SOP child waits for approval,
	// so the lane must be promoted from active to waiting via its stages.
	run := domain.Run{ID: "run-1", WorkflowID: "wf-a", Status: "running", Nodes: map[string]*domain.NodeRun{
		"plan": {Status: "running", ChildRunID: "child-1"},
	}}
	child := domain.Run{ID: "child-1", Status: "running", WorkflowSnapshot: domain.WorkflowDefinition{
		ID: "wf-child", Nodes: []domain.WorkflowNode{{ID: "approve"}},
	}, Nodes: map[string]*domain.NodeRun{"approve": {Status: "waiting_approval"}}}
	nested := map[string][]domain.Run{"run-1": {child}}
	if got := laneStatusFromRun(run); got != "active" {
		t.Fatalf("precondition: parent run status = %q, want active", got)
	}
	lane := settledRequirementLane(domain.RequirementLane{WorkflowID: "wf-a"}, run, workflows, nested, time.Now())
	if lane.Status != "waiting" {
		t.Fatalf("status = %q, want waiting", lane.Status)
	}
}

func TestSettledRequirementLaneTaskOnlyKeepsExistingStages(t *testing.T) {
	workflows := settleTestWorkflows()
	existing := []domain.RequirementNodeStage{{NodeID: "custom", Name: "已有", Status: "running"}}
	lane := settledRequirementLane(domain.RequirementLane{WorkflowID: "wf-a", LatestTaskID: "task-1", Stages: existing}, domain.Run{}, workflows, nil, time.Now())
	if !lane.Started || lane.Status != "active" {
		t.Fatalf("lane = %#v, want started active", lane)
	}
	if len(lane.Stages) != 1 || lane.Stages[0].NodeID != "custom" {
		t.Fatalf("existing stages should be kept, got %#v", lane.Stages)
	}
}

func TestSettledRequirementLaneTaskOnlyPlansStages(t *testing.T) {
	workflows := settleTestWorkflows()
	lane := settledRequirementLane(domain.RequirementLane{WorkflowID: "wf-a", LatestTaskID: "task-1"}, domain.Run{}, workflows, nil, time.Now())
	if !lane.Started || lane.Status != "active" {
		t.Fatalf("lane = %#v, want started active", lane)
	}
	if len(lane.Stages) != 2 || lane.Stages[0].Status != "pending" {
		t.Fatalf("stages = %#v, want planned stages", lane.Stages)
	}
}

func TestSettledRequirementLaneNotStarted(t *testing.T) {
	workflows := settleTestWorkflows()
	lane := settledRequirementLane(domain.RequirementLane{WorkflowID: "wf-a"}, domain.Run{}, workflows, nil, time.Now())
	if lane.Started || lane.Status != "not_started" {
		t.Fatalf("lane = %#v, want not_started", lane)
	}
	if len(lane.Stages) != 2 || lane.Stages[0].Name != "规划" || lane.Stages[1].Name != "评审" {
		t.Fatalf("stages = %#v, want planned stages", lane.Stages)
	}
}

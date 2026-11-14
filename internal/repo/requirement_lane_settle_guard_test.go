package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Only an active lane is promoted to waiting when a nested stage waits for a
// human. A run that already needs attention (failed/cancelled) or has finished
// must keep its own status even if a nested SOP child is still waiting.
func TestSettledRequirementLaneOnlyPromotesActiveRunsToWaiting(t *testing.T) {
	cases := []struct {
		name       string
		runStatus  string
		nodeStatus string
		want       string
	}{
		{name: "failed node", runStatus: "running", nodeStatus: "failed", want: "attention"},
		{name: "failed run", runStatus: "failed", nodeStatus: "succeeded", want: "attention"},
		{name: "cancelled run", runStatus: "cancelled", nodeStatus: "succeeded", want: "attention"},
		{name: "succeeded run", runStatus: "succeeded", nodeStatus: "succeeded", want: "succeeded"},
		{name: "running run", runStatus: "running", nodeStatus: "running", want: "waiting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := domain.Run{ID: "run-1", WorkflowID: "wf-a", Status: tc.runStatus, Nodes: map[string]*domain.NodeRun{
				"plan": {Status: tc.nodeStatus, ChildRunID: "child-1"},
			}}
			child := domain.Run{ID: "child-1", Status: "running", WorkflowSnapshot: domain.WorkflowDefinition{
				ID: "wf-child", Nodes: []domain.WorkflowNode{{ID: "approve"}},
			}, Nodes: map[string]*domain.NodeRun{"approve": {Status: "waiting_approval"}}}
			nested := map[string][]domain.Run{"run-1": {child}}

			lane := settledRequirementLane(domain.RequirementLane{WorkflowID: "wf-a"}, run, settleTestWorkflows(), nested, time.Now())
			if !stagesWaitForHuman(lane.Stages) {
				t.Fatalf("precondition: stages should wait for a human, got %#v", lane.Stages)
			}
			if lane.Status != tc.want {
				t.Fatalf("status = %q, want %q", lane.Status, tc.want)
			}
		})
	}
}

// A lane settled from a run takes its stages from the run, replacing any stages
// accumulated earlier, and the run path does not touch Started.
func TestSettledRequirementLaneRunReplacesExistingStages(t *testing.T) {
	run := domain.Run{ID: "run-1", WorkflowID: "wf-a", Status: "succeeded", Nodes: map[string]*domain.NodeRun{
		"plan":   {Status: "succeeded"},
		"review": {Status: "succeeded"},
	}}
	in := domain.RequirementLane{
		WorkflowID:   "wf-a",
		Started:      true,
		LatestTaskID: "task-1",
		Stages:       []domain.RequirementNodeStage{{NodeID: "stale", Status: "running"}},
	}
	lane := settledRequirementLane(in, run, settleTestWorkflows(), nil, time.Now())
	if lane.Status != "succeeded" || !lane.Started || lane.LatestTaskID != "task-1" {
		t.Fatalf("lane = %#v, want succeeded with Started/LatestTaskID preserved", lane)
	}
	if len(lane.Stages) != 2 || lane.Stages[0].NodeID != "plan" || lane.Stages[1].NodeID != "review" {
		t.Fatalf("stages = %#v, want run node stages replacing stale ones", lane.Stages)
	}
	for _, stage := range lane.Stages {
		if stage.Status != "succeeded" {
			t.Fatalf("stage %q status = %q, want succeeded", stage.NodeID, stage.Status)
		}
	}
	if len(in.Stages) != 1 || in.Stages[0].NodeID != "stale" {
		t.Fatalf("input lane must not be mutated, got %#v", in.Stages)
	}
}

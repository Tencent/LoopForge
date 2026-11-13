package repo

import (
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestWithSOPBindingCopiesBindingFieldsOnly(t *testing.T) {
	lane := domain.RequirementLane{
		BindingID: "old", Lane: "backend", Label: "后端 SOP", WorkflowID: "wf-run",
		WorkflowName: "后端 SOP", LatestRunID: "run-1", Status: "running", Started: true,
		RunCount: 2, Tokens: 42, AssigneeUserID: 1, Order: 9, DependsOn: []string{"x"},
		Handoff: []string{"y"}, Orphan: true,
		Stages: []domain.RequirementNodeStage{{NodeID: "code"}},
	}
	binding := domain.IssueSOPBinding{
		ID: "b-1", WorkflowID: "wf-bound", Order: 2, AssigneeUserID: 7,
		DependsOn: []string{"b-0"}, Handoff: []string{"prd"},
	}

	got := withSOPBinding(lane, binding)

	want := lane
	want.BindingID = "b-1"
	want.AssigneeUserID = 7
	want.Order = 2
	want.DependsOn = []string{"b-0"}
	want.Handoff = []string{"prd"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withSOPBinding() = %+v, want %+v", got, want)
	}
	if lane.BindingID != "old" || lane.Order != 9 {
		t.Fatalf("input lane mutated: %+v", lane)
	}
}

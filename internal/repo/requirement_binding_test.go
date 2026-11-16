package repo

import (
	"reflect"
	"testing"
	"time"

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

func TestBuildRequirementLanesAppliesBindingToStartedLane(t *testing.T) {
	now := time.Now().UTC()
	design := domain.WorkflowDefinition{ID: "wf-design", Name: "设计 SOP", SOPLane: "design"}
	backend := domain.WorkflowDefinition{ID: "wf-backend", Name: "后端 SOP", SOPLane: "backend", Nodes: []domain.WorkflowNode{{ID: "code", Name: "实现"}}}
	issue := domain.Issue{
		ID: "iss-started", Kind: "requirement",
		SOPBindings: []domain.IssueSOPBinding{
			{ID: "b-design", WorkflowID: design.ID, Order: 1, AssigneeUserID: 5},
			{ID: "b-backend", WorkflowID: backend.ID, Order: 2, AssigneeUserID: 9, DependsOn: []string{"b-design"}, Handoff: []string{"design.md"}},
		},
	}
	runs := []domain.Run{{
		ID: "run-b", WorkflowID: backend.ID, WorkflowSnapshot: backend, Status: "running",
		Nodes:     map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "running", StartedAt: now}},
		CreatedAt: now,
	}}
	lanes := buildRequirementLanes(issue, domain.Project{}, runs, nil, nil, map[string]domain.WorkflowDefinition{
		design.ID: design, backend.ID: backend,
	}, now.Add(time.Minute))
	if len(lanes) != 2 {
		t.Fatalf("lanes=%#v", lanes)
	}
	got := lanes[1]
	// The started lane keeps its run-derived fields and takes the binding-owned ones.
	if got.WorkflowID != backend.ID || !got.Started || got.LatestRunID != "run-b" || got.Orphan {
		t.Fatalf("started lane lost run state: %#v", got)
	}
	if got.BindingID != "b-backend" || got.AssigneeUserID != 9 || got.Order != 2 {
		t.Fatalf("started lane binding fields=%#v", got)
	}
	if !reflect.DeepEqual(got.DependsOn, []string{"b-design"}) || !reflect.DeepEqual(got.Handoff, []string{"design.md"}) {
		t.Fatalf("started lane dependsOn=%v handoff=%v", got.DependsOn, got.Handoff)
	}
}

func TestBuildRequirementLanesSingleBindingRemapsStartedRun(t *testing.T) {
	now := time.Now().UTC()
	bound := domain.WorkflowDefinition{ID: "wf-bound", Name: "绑定 SOP"}
	frozen := domain.WorkflowDefinition{ID: "wf-frozen", Name: "冻结 SOP", Nodes: []domain.WorkflowNode{{ID: "fix", Name: "修复"}}}
	issue := domain.Issue{
		ID: "iss-remap", Kind: "issue",
		SOPBindings: []domain.IssueSOPBinding{
			{ID: "b-only", WorkflowID: bound.ID, Order: 1, AssigneeUserID: 4, Handoff: []string{"fix.md"}},
		},
	}
	runs := []domain.Run{{
		ID: "run-frozen", WorkflowID: frozen.ID, WorkflowSnapshot: frozen, Status: "succeeded",
		CreatedAt: now, UpdatedAt: now,
	}}
	lanes := buildRequirementLanes(issue, domain.Project{}, runs, nil, nil, map[string]domain.WorkflowDefinition{
		bound.ID: bound, frozen.ID: frozen,
	}, now.Add(time.Minute))
	if len(lanes) != 1 {
		t.Fatalf("lanes=%#v", lanes)
	}
	got := lanes[0]
	// The frozen run becomes the single slot but must carry the binding's identity.
	if got.WorkflowID != frozen.ID || got.LatestRunID != "run-frozen" || got.Orphan {
		t.Fatalf("remapped lane=%#v", got)
	}
	if got.BindingID != "b-only" || got.AssigneeUserID != 4 || got.Order != 1 || !reflect.DeepEqual(got.Handoff, []string{"fix.md"}) {
		t.Fatalf("remapped lane binding fields=%#v", got)
	}
}

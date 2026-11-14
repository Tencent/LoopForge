package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestResolvedRequirementWorkflowPrefersSnapshot(t *testing.T) {
	catalog := domain.WorkflowDefinition{ID: "wf-a", Name: "目录 A"}
	snapshot := domain.WorkflowDefinition{ID: "wf-a", Name: "快照 A"}
	if got := resolvedRequirementWorkflow(map[string]domain.WorkflowDefinition{"wf-a": catalog}, "wf-a", snapshot); got.Name != "快照 A" {
		t.Fatalf("snapshot should win, got %#v", got)
	}
}

func TestResolvedRequirementWorkflowFallsBackToCatalog(t *testing.T) {
	catalog := domain.WorkflowDefinition{ID: "wf-a", Name: "目录 A"}
	if got := resolvedRequirementWorkflow(map[string]domain.WorkflowDefinition{"wf-a": catalog}, "wf-a", domain.WorkflowDefinition{}); got.Name != "目录 A" {
		t.Fatalf("catalog fallback failed, got %#v", got)
	}
}

func TestResolvedRequirementWorkflowUnknownReturnsZero(t *testing.T) {
	got := resolvedRequirementWorkflow(map[string]domain.WorkflowDefinition{}, "wf-missing", domain.WorkflowDefinition{})
	if got.ID != "" || got.Name != "" {
		t.Fatalf("unknown workflow should resolve to zero value, got %#v", got)
	}
}

func TestRequirementLaneNameFallsBackToID(t *testing.T) {
	if got := requirementLaneName(domain.WorkflowDefinition{ID: "wf-a", Name: "名字 A"}, "wf-a"); got != "名字 A" {
		t.Fatalf("want workflow name, got %q", got)
	}
	if got := requirementLaneName(domain.WorkflowDefinition{ID: "wf-a"}, "wf-a"); got != "wf-a" {
		t.Fatalf("want id fallback, got %q", got)
	}
	if got := requirementLaneName(domain.WorkflowDefinition{}, "wf-unknown"); got != "wf-unknown" {
		t.Fatalf("want id for unknown workflow, got %q", got)
	}
}

// The two helpers must reproduce the lane naming and lane classification the
// bindings path produced inline before the extraction.
func TestBuildRequirementLanesNamesUnknownBindingByID(t *testing.T) {
	now := time.Now().UTC()
	issue := domain.Issue{
		ID: "iss-unknown-wf", Kind: "requirement",
		SOPBindings: []domain.IssueSOPBinding{{ID: "b-1", WorkflowID: "wf-gone", Order: 1}},
	}
	lanes := buildRequirementLanes(issue, domain.Project{}, nil, nil, nil, map[string]domain.WorkflowDefinition{}, now)
	if len(lanes) != 1 {
		t.Fatalf("lanes=%#v", lanes)
	}
	got := lanes[0]
	if got.WorkflowName != "wf-gone" || got.Label != "wf-gone" {
		t.Fatalf("unknown binding lane should fall back to its id: %#v", got)
	}
	if got.Lane != domain.InferSOPLane(domain.WorkflowDefinition{ID: "wf-gone"}) {
		t.Fatalf("lane classification=%q", got.Lane)
	}
}

// A run whose workflow has left the catalog still gets a lane named after the
// frozen snapshot rather than being dropped.
func TestBuildRequirementLanesKeepsRunSnapshotName(t *testing.T) {
	now := time.Now().UTC()
	frozen := domain.WorkflowDefinition{ID: "wf-frozen", Name: "冻结 SOP", Nodes: []domain.WorkflowNode{{ID: "code", Name: "实现"}}}
	runs := []domain.Run{{
		ID: "run-frozen", WorkflowID: frozen.ID, WorkflowSnapshot: frozen, Status: "succeeded",
		CreatedAt: now, UpdatedAt: now,
	}}
	lanes := buildRequirementLanes(domain.Issue{ID: "iss-frozen"}, domain.Project{}, runs, nil, nil, map[string]domain.WorkflowDefinition{}, now.Add(time.Minute))
	if len(lanes) != 1 {
		t.Fatalf("lanes=%#v", lanes)
	}
	if got := lanes[0]; got.WorkflowName != "冻结 SOP" || got.Label != "冻结 SOP" || !got.Started {
		t.Fatalf("lane=%#v", got)
	}
}

package domain

import "testing"

func TestInferSOPLane(t *testing.T) {
	cases := []struct {
		wf   WorkflowDefinition
		want string
	}{
		{WorkflowDefinition{SOPLane: "frontend"}, SOPLaneFrontend},
		{WorkflowDefinition{ID: "generic-product-delivery"}, SOPLaneProduct},
		{WorkflowDefinition{ID: "proj-fe", Tags: []string{"generic-source:generic-frontend-delivery"}}, SOPLaneFrontend},
		{WorkflowDefinition{ID: "generic-backend-delivery"}, SOPLaneBackend},
		{WorkflowDefinition{ID: "requirement-development-production"}, SOPLaneBackend},
		{WorkflowDefinition{ID: "issue-fix-production"}, SOPLaneRepair},
		{WorkflowDefinition{Name: "需求开发 · 生产闭环"}, SOPLaneBackend},
		{WorkflowDefinition{Name: "设计交付"}, SOPLaneDesign},
	}
	for _, tc := range cases {
		if got := InferSOPLane(tc.wf); got != tc.want {
			t.Fatalf("InferSOPLane(%+v)=%q want %q", tc.wf, got, tc.want)
		}
	}
}

func TestIsRequirementWorkItem(t *testing.T) {
	if !IsRequirementWorkItem(Issue{Kind: "requirement", Source: "tapd"}) {
		t.Fatal("TAPD requirement")
	}
	if IsRequirementWorkItem(Issue{Kind: "issue", Source: "gongfeng"}) {
		t.Fatal("gongfeng issue is not a requirement")
	}
}

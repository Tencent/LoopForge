package engine

import (
	"testing"

	"agentflow/internal/domain"
)

func TestFirstNodeWithStatus(t *testing.T) {
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	run := func(states map[string]string) domain.Run {
		r := domain.Run{Nodes: map[string]*domain.NodeRun{}}
		for id, status := range states {
			r.Nodes[id] = &domain.NodeRun{Status: status}
		}
		return r
	}
	cases := []struct {
		name   string
		states map[string]string
		status string
		wantID string
		wantOK bool
	}{
		{"no match", map[string]string{"a": "succeeded", "b": "running"}, "failed", "", false},
		{"single match", map[string]string{"a": "succeeded", "b": "failed"}, "failed", "b", true},
		{"definition order wins", map[string]string{"a": "cancelled", "c": "cancelled"}, "cancelled", "a", true},
		{"undefined run node ignored", map[string]string{"x": "waiting_approval"}, "waiting_approval", "", false},
		{"missing run nodes", nil, "failed", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := firstNodeWithStatus(wf, run(tc.states), tc.status)
			if ok != tc.wantOK || got.ID != tc.wantID {
				t.Fatalf("firstNodeWithStatus = (%q, %v), want (%q, %v)", got.ID, ok, tc.wantID, tc.wantOK)
			}
		})
	}
	r := run(map[string]string{"a": "waiting_approval", "b": "failed", "c": "cancelled"})
	if !hasWaiting(wf, r) || !hasFailed(wf, r) {
		t.Fatal("hasWaiting/hasFailed should report matching nodes")
	}
	if n, ok := cancelledNode(wf, r); !ok || n.ID != "c" {
		t.Fatalf("cancelledNode = (%q, %v), want (c, true)", n.ID, ok)
	}
	if hasWaiting(wf, run(nil)) || hasFailed(wf, run(nil)) {
		t.Fatal("empty run should report no waiting/failed nodes")
	}
}

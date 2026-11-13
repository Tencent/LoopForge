package botinteraction

import (
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestSnapshotNode(t *testing.T) {
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{
		{ID: "a", Name: "A"}, {ID: "b", Name: "B", Type: "script"},
	}}}
	if node, ok := snapshotNode(run, "b"); !ok || node.Name != "B" || node.Type != "script" {
		t.Fatalf("snapshotNode(b) = %#v, %v", node, ok)
	}
	if node, ok := snapshotNode(run, "missing"); ok || node.ID != "" {
		t.Fatalf("snapshotNode(missing) = %#v, %v", node, ok)
	}
	if _, ok := snapshotNode(domain.Run{}, "a"); ok {
		t.Fatal("empty snapshot must not match")
	}
}

func TestCardReworkTargetMissing(t *testing.T) {
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{
		{ID: "review", RejectTo: "gone"}, {ID: "plain"}, {ID: "fix", Name: "修复"},
	}}}
	for _, id := range []string{"review", "plain", "unknown"} {
		if got := cardReworkTarget(run, id); got != "" {
			t.Fatalf("cardReworkTarget(%s) = %q, want empty", id, got)
		}
	}
}

func TestActionsForDeliverySnapshotButtons(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	keys := func(actions []map[string]any) map[string]bool {
		out := map[string]bool{}
		for _, a := range actions {
			out[a["key"].(string)] = true
		}
		return out
	}
	cases := []struct {
		name    string
		def     domain.WorkflowNode
		status  string
		event   string
		key     string
		present bool
	}{
		{"reject shown", domain.WorkflowNode{ID: "n", RejectTo: "fix"}, "waiting_approval", "waiting", "reject", true},
		{"reject hidden", domain.WorkflowNode{ID: "n"}, "waiting_approval", "waiting", "reject", false},
		{"reject other node", domain.WorkflowNode{ID: "other", RejectTo: "fix"}, "waiting_approval", "waiting", "reject", false},
		{"pass shown", domain.WorkflowNode{ID: "n", Type: "script"}, "failed", "failed", "pass", true},
		{"pass hidden", domain.WorkflowNode{ID: "n", Type: "agent"}, "failed", "failed", "pass", false},
		{"pass other node", domain.WorkflowNode{ID: "other", Type: "script"}, "failed", "failed", "pass", false},
	}
	for i, tc := range cases {
		run := domain.Run{
			ID: "snapshot-buttons-" + string(rune('a'+i)), Status: "waiting",
			WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{tc.def}},
			Nodes:            map[string]*domain.NodeRun{"n": {NodeID: "n", Status: tc.status}},
			CreatedAt:        time.Now(),
		}
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
		actions := ActionsForDelivery(st, domain.NotificationDelivery{RunID: run.ID, NodeID: "n", EventType: tc.event})
		if got := keys(actions)[tc.key]; got != tc.present {
			t.Fatalf("%s: %s present = %v, actions = %#v", tc.name, tc.key, got, actions)
		}
	}
}

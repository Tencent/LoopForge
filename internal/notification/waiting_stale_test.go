package notification

import (
	"testing"

	"agentflow/internal/domain"
)

func TestWaitingNodeStale(t *testing.T) {
	item := domain.NotificationDelivery{NodeID: "n", Attempt: 2, ReworkCount: 1}
	waiting := func() *domain.NodeRun {
		return &domain.NodeRun{NodeID: "n", Status: "waiting_approval", Attempt: 2, ReworkCount: 1}
	}
	cases := []struct {
		name   string
		status string
		node   func() *domain.NodeRun
		want   bool
	}{
		{"still waiting", "waiting", waiting, false},
		{"terminal run", "succeeded", waiting, true},
		{"node missing", "waiting", func() *domain.NodeRun { return nil }, true},
		{"node no longer waiting", "running", func() *domain.NodeRun { n := waiting(); n.Status = "running"; return n }, true},
		{"newer attempt", "waiting", func() *domain.NodeRun { n := waiting(); n.Attempt = 3; return n }, true},
		{"newer rework", "waiting", func() *domain.NodeRun { n := waiting(); n.ReworkCount = 2; return n }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := domain.Run{Status: tc.status, Nodes: map[string]*domain.NodeRun{}}
			if node := tc.node(); node != nil {
				run.Nodes["n"] = node
			}
			if got := waitingNodeStale(run, item); got != tc.want {
				t.Fatalf("waitingNodeStale() = %v, want %v", got, tc.want)
			}
		})
	}
	if !waitingNodeStale(domain.Run{}, item) {
		t.Fatal("zero-value run (deleted) must be stale")
	}
}

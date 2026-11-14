package httpapi

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A failed step is stamped with the time it finished, not when it started,
// so the thread shows the failure where it actually happened.
func TestRoomNodeMessageFailedPrefersFinishTime(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	nr := &domain.NodeRun{Status: "failed", Attempt: 1, StartedAt: now.Add(-2 * time.Minute), FinishedAt: now.Add(-time.Minute)}
	msg, ok := roomNodeMessage(domain.ChatMessage{}, domain.Run{ID: "r", UpdatedAt: now}, domain.WorkflowNode{ID: "n", Type: "script"}, nr, "Flow", func(string) string { return "" })
	if !ok {
		t.Fatal("failed node produced no message")
	}
	if !msg.CreatedAt.Equal(nr.FinishedAt) {
		t.Fatalf("failed time = %v, want finished %v", msg.CreatedAt, nr.FinishedAt)
	}
	if msg.NodeID != "n" || msg.NodeStatus != "failed" {
		t.Fatalf("node fields: id=%q status=%q", msg.NodeID, msg.NodeStatus)
	}
}

// Only agent nodes take the agent's name; a script node that happens to
// carry an AgentID still speaks as the SOP.
func TestRoomNodeMessageAgentNameOnlyForAgentNodes(t *testing.T) {
	lookup := func(id string) string {
		if id == "agent" {
			return "Agent Name"
		}
		return ""
	}
	nr := &domain.NodeRun{Status: "failed"}
	msg, _ := roomNodeMessage(domain.ChatMessage{}, domain.Run{ID: "r"}, domain.WorkflowNode{ID: "s", Type: "script", AgentID: "agent"}, nr, "Flow", lookup)
	if msg.Author != "SOP「Flow」" {
		t.Fatalf("script node author = %q, want SOP", msg.Author)
	}
	msg, _ = roomNodeMessage(domain.ChatMessage{}, domain.Run{ID: "r"}, domain.WorkflowNode{ID: "a", Type: "agent", AgentID: "  "}, nr, "Flow", func(string) string {
		t.Fatal("blank AgentID must not be looked up")
		return ""
	})
	if msg.Author != "SOP「Flow」" {
		t.Fatalf("blank agent author = %q, want SOP", msg.Author)
	}
}

// Approval prompts always come from the SOP, even when the waiting node is
// an agent node whose failures would be attributed to the agent.
func TestRoomNodeMessageApprovalSpeaksAsSOPForAgentNodes(t *testing.T) {
	node := domain.WorkflowNode{ID: "review", Name: "Review", Type: "agent", AgentID: "agent"}
	msg, ok := roomNodeMessage(domain.ChatMessage{}, domain.Run{ID: "r"}, node, &domain.NodeRun{Status: "waiting_approval", Attempt: 3}, "Flow", func(string) string { return "Agent Name" })
	if !ok {
		t.Fatal("waiting approval produced no message")
	}
	if msg.Author != "SOP「Flow」" || msg.Kind != domain.ChatKindApproval || msg.NodeStatus != "waiting_approval" || msg.NodeID != "review" {
		t.Fatalf("approval message: %#v", msg)
	}
}

// A message stamped exactly at the floor (the dispatch card time) must still
// be pushed after it, otherwise the step can sort before or tie with the card.
func TestOrderRoomMessagesAfterMovesTimesEqualToFloor(t *testing.T) {
	floor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := []domain.ChatMessage{{CreatedAt: floor}}
	orderRoomMessagesAfter(out, floor)
	if want := floor.Add(time.Millisecond); !out[0].CreatedAt.Equal(want) {
		t.Fatalf("time = %v, want %v", out[0].CreatedAt, want)
	}

	// Two steps sharing one clock reading stay strictly increasing.
	later := floor.Add(time.Hour)
	same := []domain.ChatMessage{{CreatedAt: later}, {CreatedAt: later}}
	orderRoomMessagesAfter(same, floor)
	if !same[0].CreatedAt.Equal(later) || !same[1].CreatedAt.After(same[0].CreatedAt) {
		t.Fatalf("equal times not ordered: %v, %v", same[0].CreatedAt, same[1].CreatedAt)
	}
}

package httpapi

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRoomNodeMessageSkipsQuietStatuses(t *testing.T) {
	node := domain.WorkflowNode{ID: "n", Name: "Step", Type: "script"}
	for _, status := range []string{"succeeded", "running", "pending", ""} {
		if msg, ok := roomNodeMessage(domain.ChatMessage{}, domain.Run{ID: "r"}, node, &domain.NodeRun{Status: status}, "Flow", func(string) string { return "" }); ok {
			t.Fatalf("status %q produced message: %#v", status, msg)
		}
	}
}

func TestRoomNodeMessageFailed(t *testing.T) {
	now := time.Now().UTC()
	base := domain.ChatMessage{RoomID: "room", RunID: "r"}
	run := domain.Run{ID: "r", UpdatedAt: now}
	node := domain.WorkflowNode{ID: "bad", Type: "agent", AgentID: "agent"}
	nr := &domain.NodeRun{Status: "failed", Attempt: 2, Error: strings.Repeat("e", 400), StartedAt: now.Add(-time.Minute)}
	msg, ok := roomNodeMessage(base, run, node, nr, "Flow", func(id string) string {
		if id == "agent" {
			return "Agent Name"
		}
		return ""
	})
	if !ok {
		t.Fatal("failed node produced no message")
	}
	if msg.RoomID != "room" || msg.Author != "Agent Name" || msg.NodeName != "bad" || msg.Attempt != 2 ||
		msg.Kind != domain.ChatKindProgress || msg.ID != roomProgressID("r", "bad", "failed", "2") {
		t.Fatalf("failed message: %#v", msg)
	}
	if want := "「bad」失败：" + strings.Repeat("e", 280) + "…"; msg.Body != want {
		t.Fatalf("failed body = %q", msg.Body)
	}
	if !msg.CreatedAt.Equal(nr.StartedAt) {
		t.Fatalf("failed time = %v, want started %v", msg.CreatedAt, nr.StartedAt)
	}

	msg, _ = roomNodeMessage(base, run, domain.WorkflowNode{ID: "s", Name: "Script", Type: "agent", AgentID: "unknown"},
		&domain.NodeRun{Status: "failed"}, "Flow", func(string) string { return "" })
	if msg.Author != "SOP「Flow」" || msg.Body != "「Script」失败" || !msg.CreatedAt.Equal(now) {
		t.Fatalf("fallback failed message: %#v", msg)
	}
}

func TestRoomNodeMessageApproval(t *testing.T) {
	now := time.Now().UTC()
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{
		{ID: "bad", Type: "agent"},
		{ID: "approve", Name: "Approve", Type: "approval", RejectTo: "bad"},
		{ID: "plain", Name: "Plain", Type: "approval"},
	}}
	run := domain.Run{ID: "r", WorkflowSnapshot: wf, UpdatedAt: now}
	nr := &domain.NodeRun{Status: "waiting_approval", Attempt: 1, StartedAt: now.Add(-time.Minute)}
	msg, ok := roomNodeMessage(domain.ChatMessage{}, run, wf.Nodes[1], nr, "Flow", func(string) string { return "Agent" })
	if !ok || msg.Kind != domain.ChatKindApproval || msg.Author != "SOP「Flow」" ||
		msg.Body != "「Approve」等待审批，可以直接在群里批准或打回" || msg.ID != roomProgressID("r", "approve", "approval", "1") ||
		!msg.CreatedAt.Equal(nr.StartedAt) {
		t.Fatalf("approval message: %#v", msg)
	}
	msg, _ = roomNodeMessage(domain.ChatMessage{}, run, wf.Nodes[2], &domain.NodeRun{Status: "waiting_approval"}, "Flow", func(string) string { return "" })
	if msg.Body != "「Plain」等待审批，可以直接在群里批准" || !msg.CreatedAt.Equal(now) {
		t.Fatalf("approval without reject target: %#v", msg)
	}
}

func TestOrderRoomMessagesAfter(t *testing.T) {
	floor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := floor.Add(time.Hour)
	out := []domain.ChatMessage{
		{CreatedAt: floor.Add(-time.Minute)},
		{CreatedAt: floor},
		{CreatedAt: late},
		{CreatedAt: late.Add(-time.Second)},
	}
	orderRoomMessagesAfter(out, floor)
	want := []time.Time{floor.Add(time.Millisecond), floor.Add(3 * time.Millisecond), late, late.Add(4 * time.Millisecond)}
	for i := range out {
		if !out[i].CreatedAt.Equal(want[i]) {
			t.Fatalf("message %d time = %v, want %v", i, out[i].CreatedAt, want[i])
		}
	}

	unordered := []domain.ChatMessage{{CreatedAt: late}, {CreatedAt: floor}}
	orderRoomMessagesAfter(unordered, time.Time{})
	if !unordered[0].CreatedAt.Equal(late) || !unordered[1].CreatedAt.Equal(late.Add(2*time.Millisecond)) {
		t.Fatalf("zero floor ordering: %#v", unordered)
	}
	orderRoomMessagesAfter(nil, floor)
}

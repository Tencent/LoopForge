package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestSpaceAIPromptAndPureHelpers(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: "cvb-r2-ai-room", ProjectID: "platform", Name: "AI Room"}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "cvb-r2-ai-issue", ProjectID: "platform", Number: 77, Title: "Requirement",
		Description: "Detailed requirement", Status: "todo", Priority: "high"}
	if err := f.s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	space := domain.RequirementSpace{ID: room.ID, ProjectID: room.ProjectID, IssueID: issue.ID, Summary: "Space summary"}
	if err := f.s.PutSpace(space); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutSpaceMember(domain.SpaceMember{SpaceID: room.ID, UserID: f.user.ID, Name: "Member", Title: "Owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PutSpaceArtifact(domain.SpaceArtifact{ID: "cvb-r2-doc", SpaceID: room.ID, Kind: "doc", Title: "Doc",
		Summary: "summary", Body: "document body"}, 0); err != nil {
		t.Fatal(err)
	}
	old := domain.ChatMessage{ID: "cvb-r2-old", RoomID: room.ID, Kind: "text", Author: "Member", Body: "Earlier",
		CreatedAt: time.Now().Add(-time.Minute)}
	if err := f.s.PutMessage(old); err != nil {
		t.Fatal(err)
	}
	msg := domain.ChatMessage{ID: "cvb-r2-now", RoomID: room.ID, Kind: "text", Author: "Member", Body: "Current",
		Attachments: []domain.AssistantAttachment{{Name: "spec.md"}}, CreatedAt: time.Now()}
	if err := f.s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}
	h := &spacesHandlers{deps: &deps{repo: f.s}}
	prompt := h.aiPrompt(room, space, msg)
	for _, want := range []string{"Requirement", "Detailed requirement", "Space summary", "Member", "document body", "Earlier", "spec.md", spaceDeliverablePrompt} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if got := h.aiPrompt(room, domain.RequirementSpace{ID: room.ID, ProjectID: room.ProjectID}, msg); !strings.Contains(got, "还没有关联需求") {
		t.Fatal("unlinked requirement prompt missing")
	}

	req := assistantRequest{SpaceID: room.ID}
	if got := withSpaceDeliverable(req, "base"); !strings.Contains(got, spaceDeliverablePrompt) {
		t.Fatal("deliverable prompt missing")
	}
	if withSpaceDeliverable(assistantRequest{}, "base") != "base" ||
		spaceDeliverableSuffix(nil) != "" ||
		!strings.Contains(spaceDeliverableSuffix(&AgentToolSession{SpaceID: room.ID}), spaceDeliverablePrompt) {
		t.Fatal("deliverable helpers")
	}
	if apiError([]byte(`{"error":"specific"}`), "fallback") != "specific" ||
		apiError([]byte(`bad`), "fallback") != "fallback" {
		t.Fatal("apiError")
	}
	for status, want := range map[string]string{
		"running": "运行中", "waiting_approval": "等待审批", "succeeded": "已完成", "failed": "失败",
		"cancelled": "已取消", "pending": "排队中", "queued": "排队中", "custom": "custom",
	} {
		if laneStatusText(status) != want {
			t.Errorf("lane status %q", status)
		}
	}
	if indent("a\nb") != "  a\n  b" || attachmentNote(nil) != "" ||
		!strings.Contains(attachmentNote(msg.Attachments), "spec.md") {
		t.Fatal("space text helpers")
	}
}

func TestSpaceAIReconcileStates(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: "cvb-r2-reconcile", ProjectID: "platform", Name: "Reconcile"}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	h := &spacesHandlers{deps: &deps{repo: f.s}}
	now := time.Now().UTC()
	messages := []domain.ChatMessage{
		{ID: "ordinary", RoomID: room.ID, Kind: "text", AIStatus: domain.AIStatusRunning},
		{ID: "missing-old", RoomID: room.ID, Kind: domain.ChatKindAI, AIStatus: domain.AIStatusRunning, AIRequesterID: 1, AISessionID: "missing-old", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "missing-new", RoomID: room.ID, Kind: domain.ChatKindAI, AIStatus: domain.AIStatusRunning, AIRequesterID: 1, AISessionID: "missing-new", CreatedAt: now},
	}
	for _, m := range messages {
		_ = f.s.PutMessage(m)
	}
	addGeneration := func(session, status string, reply assistantReply, generationError string, started time.Time) {
		raw, _ := json.Marshal(reply)
		g := domain.AssistantGeneration{ID: "gen-" + session, SessionID: session, UserID: 1, Status: "running", StartedAt: started}
		if err := f.s.BeginAssistantGeneration(g); err != nil {
			t.Fatal(err)
		}
		g.Status, g.Reply, g.Error = status, raw, generationError
		if status == "running" {
			g.Events = []domain.AssistantGenerationEvent{{Sequence: 1}}
		}
		if err := f.s.CheckpointAssistantGeneration(g); err != nil {
			t.Fatal(err)
		}
		m := domain.ChatMessage{ID: "msg-" + session, RoomID: room.ID, Kind: domain.ChatKindAI, AIStatus: domain.AIStatusRunning,
			AIRequesterID: 1, AISessionID: session, CreatedAt: started}
		if err := f.s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, m)
	}
	addGeneration("complete", "completed", assistantReply{Message: "done"}, "", now)
	addGeneration("blank", "completed", assistantReply{}, "", now)
	addGeneration("drafts", "completed", assistantReply{Message: "draft", Drafts: []assistantDraft{{Tool: "project"}}}, "", now)
	addGeneration("failed", "failed", assistantReply{}, "boom", now)
	addGeneration("cancelled", "cancelled", assistantReply{}, "", now)
	addGeneration("timeout", "running", assistantReply{}, "", now.Add(-spaceAITimeout-time.Minute))
	addGeneration("progress", "running", assistantReply{}, "", now)
	got := h.reconcileAI(room, messages)
	if len(got) != len(messages) {
		t.Fatalf("reconciled len %d", len(got))
	}
	status := map[string]domain.ChatMessage{}
	for _, m := range got {
		status[m.ID] = m
	}
	if status["missing-old"].AIStatus != domain.AIStatusFailed ||
		status["msg-complete"].AIStatus != domain.AIStatusCompleted ||
		status["msg-blank"].Body == "" ||
		status["msg-failed"].AIStatus != domain.AIStatusFailed ||
		status["msg-timeout"].AIStatus != domain.AIStatusFailed ||
		status["msg-progress"].AIProgress == "" {
		t.Fatalf("unexpected reconciliation: %#v", status)
	}
}

func TestRoomRunProjectionCacheAndTargets(t *testing.T) {
	now := time.Now().UTC()
	wf := domain.WorkflowDefinition{Name: "Flow", Nodes: []domain.WorkflowNode{
		{ID: "ok", Name: "Done", Type: "script"},
		{ID: "bad", Name: "Failed", Type: "agent", AgentID: "agent"},
		{ID: "approve", Name: "Approve", Type: "approval", RejectTo: "bad"},
	}, Edges: []domain.WorkflowEdge{{Source: "bad", Target: "approve"}}}
	run := domain.Run{ID: "cvb-r2-room-run", WorkflowID: "wf", WorkflowSnapshot: wf, Status: "failed", UpdatedAt: now,
		Nodes: map[string]*domain.NodeRun{
			"ok": {Status: "succeeded"}, "bad": {Status: "failed", Attempt: 2, Error: strings.Repeat("e", 400)},
			"approve": {Status: "waiting_approval", Attempt: 1, StartedAt: now.Add(-time.Minute)},
		}}
	dispatch := domain.ChatMessage{ID: "dispatch", RoomID: "room", WorkflowName: "Flow", CreatedAt: now}
	projected := roomRunMessages(domain.ChatRoom{ID: "room"}, dispatch, run, func(id string) string {
		if id == "agent" {
			return "Agent Name"
		}
		return ""
	})
	if len(projected) != 3 || projected[0].Author != "Agent Name" || projected[2].Body != "运行失败" {
		t.Fatalf("projected: %#v", projected)
	}
	if roomProgressID("a") != roomProgressID("a") || roomTrim("abc", 2) != "ab…" ||
		roomRejectTarget(wf, "approve") != "bad" {
		t.Fatal("room run helpers")
	}
	if roomRejectTarget(domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "z", Target: "n"}, {Source: "a", Target: "n"}}}, "n") != "a" ||
		roomRejectTarget(domain.WorkflowDefinition{}, "n") != "" {
		t.Fatal("room reject fallback")
	}

	var progress domain.ChatMessage
	roomRunProgress(&progress, run)
	if progress.RunTotal != 3 || progress.RunCompleted != 1 || len(progress.RunCurrent) != 1 {
		t.Fatalf("progress: %+v", progress)
	}
	if !roomApprovalResolved(domain.ChatMessage{NodeID: "missing"}, run) ||
		roomApprovalResolved(domain.ChatMessage{NodeID: "approve", Attempt: 1}, domain.Run{Status: "running", Nodes: run.Nodes}) {
		t.Fatal("approval resolution")
	}
	cache := &roomRunCache{}
	cache.put(run.ID, run)
	if got, ok := cache.get(run.ID, run.Revision); !ok || got.ID != run.ID {
		t.Fatal("cache miss")
	}
	if _, ok := cache.get(run.ID, run.Revision+1); ok {
		t.Fatal("stale cache hit")
	}
	for i := 0; i < roomRunCacheSize+2; i++ {
		cache.put(roomProgressID(string(rune(i))), domain.Run{Revision: i})
	}
	if len(cache.entries) >= roomRunCacheSize {
		t.Fatalf("cache was not bounded: %d", len(cache.entries))
	}
	if firstTime(time.Time{}).IsZero() {
		t.Fatal("zero-time fallback was zero")
	}
	missingNodeRun := domain.Run{ID: "missing-node", WorkflowSnapshot: domain.WorkflowDefinition{
		Nodes: []domain.WorkflowNode{{ID: "missing"}},
	}, Nodes: map[string]*domain.NodeRun{}}
	if messages := roomRunMessages(domain.ChatRoom{ID: "room"}, dispatch, missingNodeRun, func(string) string { return "" }); len(messages) != 0 {
		t.Fatalf("missing node projected: %#v", messages)
	}
	var missingProgress domain.ChatMessage
	roomRunProgress(&missingProgress, missingNodeRun)
	if missingProgress.RunCompleted != 0 {
		t.Fatal("missing node counted complete")
	}
	f := cvBNewHTTPFixture(t)
	rooms := &roomsHandlers{deps: &deps{repo: f.s}}
	if got := rooms.syncRoomRuns(domain.ChatRoom{ID: "empty"}, nil); got != nil {
		t.Fatalf("empty room synchronization: %#v", got)
	}
}

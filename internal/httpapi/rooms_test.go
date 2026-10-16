package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestRoomDispatchesGenericTemplate(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "room-test-session-secret")
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutGenericTemplate(repo.GenericTemplate{Workflow: domain.WorkflowDefinition{
		ID: "room-generic", Name: "通用确认", Version: 1, Status: "published", Generic: true,
		SandboxProfileID: "local",
		Nodes:            []domain.WorkflowNode{{ID: "approval", Name: "人工确认", Type: "approval", MaxAttempts: 1}},
	}}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	owner, _ := s.User(1)
	room := requestJSONAuthed[domain.ChatRoomView](t, h, owner, http.MethodPost, "/api/rooms", map[string]any{
		"projectId": "platform", "name": "通用模板群",
	}, http.StatusCreated)

	got := requestJSONAuthed[roomIssueResult](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/issues", map[string]any{
		"title": "用通用模板跑", "workflowId": "room-generic",
	}, http.StatusCreated)
	if got.Issue.WorkflowID != "room-generic" || got.Message.WorkflowID != "room-generic" {
		t.Fatalf("generic template should be accepted as the dispatch target: %+v", got)
	}
	if !strings.Contains(got.DispatchError, "代码仓库") {
		t.Fatalf("without a bound repository the engine's reason should surface on the card: %+v", got)
	}
}

func TestRoomCreatesIssueAndDispatchesSOPOrRole(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "room-test-session-secret")
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutWorkflow(domain.WorkflowDefinition{
		ID: "room-sop", Name: "群聊交付", Version: 1, ProjectID: "platform", Status: "published",
		SandboxProfileID: "local",
		Nodes:            []domain.WorkflowNode{{ID: "approval", Name: "人工确认", Type: "approval", MaxAttempts: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{
		ID: "room-sop-2", Name: "群聊评审", Version: 1, ProjectID: "platform", Status: "published",
		SandboxProfileID: "local",
		Nodes:            []domain.WorkflowNode{{ID: "review", Name: "人工评审", Type: "approval", MaxAttempts: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	owner, _ := s.User(1)

	room := requestJSONAuthed[domain.ChatRoomView](t, h, owner, http.MethodPost, "/api/rooms", map[string]any{
		"projectId": "platform", "name": "体验空间",
	}, http.StatusCreated)
	if room.ID == "" || room.ProjectID != "platform" || room.Name != "体验空间" || len(room.Members) == 0 {
		t.Fatalf("unexpected room: %+v", room)
	}

	msg := requestJSONAuthed[domain.ChatMessage](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/messages", map[string]any{
		"body": "这条需求今天要排进去",
	}, http.StatusCreated)
	if msg.Kind != "text" || msg.Author == "" || msg.Body == "" || msg.CreatedAt.IsZero() {
		t.Fatalf("unexpected message: %+v", msg)
	}

	roleOnly := requestJSONAuthed[roomIssueResult](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/issues", map[string]any{
		"title": "交给审查角色", "description": "先看方案", "agentId": "reviewer",
	}, http.StatusCreated)
	if roleOnly.Issue.AssigneeType != "agent" || roleOnly.Issue.AssigneeID != "reviewer" || roleOnly.Task != nil || roleOnly.DispatchError != "" {
		t.Fatalf("role dispatch should assign without starting a run: %+v", roleOnly)
	}
	if roleOnly.Message.TargetType != "role" || roleOnly.Message.IssueID != roleOnly.Issue.ID {
		t.Fatalf("role card: %+v", roleOnly.Message)
	}
	if s.IssueHasTask(roleOnly.Issue.ID) {
		t.Fatal("role-only dispatch started a task")
	}

	dispatched := requestJSONAuthed[roomIssueResult](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/issues", map[string]any{
		"title": "按 SOP 交付", "description": "走群聊派单", "workflowId": "room-sop", "agentId": "developer",
	}, http.StatusCreated)
	if dispatched.Task == nil || dispatched.Task.RunID == "" || dispatched.Task.IssueID != dispatched.Issue.ID {
		t.Fatalf("sop dispatch: %+v", dispatched)
	}
	if dispatched.Issue.WorkflowID != "room-sop" || dispatched.Issue.AssigneeID != "developer" {
		t.Fatalf("issue binding: %+v", dispatched.Issue)
	}
	if dispatched.Message.TargetType != "both" || dispatched.Message.RunID == "" {
		t.Fatalf("dispatch card: %+v", dispatched.Message)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		run, ok := s.RunState(dispatched.Task.RunID)
		if ok && run.Nodes["approval"] != nil && run.Nodes["approval"].Status == "waiting_approval" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dispatch did not reach its approval gate: %+v", run)
		}
		time.Sleep(10 * time.Millisecond)
	}

	messages := requestJSONAuthed[[]domain.ChatMessage](t, h, owner, http.MethodGet, "/api/rooms/"+room.ID+"/messages", nil, http.StatusOK)
	if len(messages) != 4 {
		t.Fatalf("messages: %+v", messages)
	}
	card, approval := messages[2], messages[3]
	if card.Kind != domain.ChatKindDispatch || card.RunStatus == "" || card.RunTotal != 1 || len(card.RunCurrent) != 1 {
		t.Fatalf("dispatch card should carry live run progress: %+v", card)
	}
	if approval.Kind != domain.ChatKindApproval || approval.NodeID != "approval" || approval.RunID != dispatched.Task.RunID || approval.Resolved {
		t.Fatalf("waiting step should post an actionable approval card after the dispatch: %+v", approval)
	}
	if approval.CanReject {
		t.Fatalf("the only step has nowhere to send work back, so reject must not be offered: %+v", approval)
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/decisions", map[string]any{
		"runId": dispatched.Task.RunID, "nodeId": "approval", "action": "reject",
	}, http.StatusBadRequest)
	again := requestJSONAuthed[[]domain.ChatMessage](t, h, owner, http.MethodGet, "/api/rooms/"+room.ID+"/messages", nil, http.StatusOK)
	if len(again) != 4 {
		t.Fatalf("re-reading must not duplicate progress: %d", len(again))
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/decisions", map[string]any{
		"runId": "run-not-from-room", "nodeId": "approval", "action": "approve",
	}, http.StatusForbidden)
	decision := requestJSONAuthed[domain.ChatMessage](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/decisions", map[string]any{
		"runId": dispatched.Task.RunID, "nodeId": "approval", "action": "approve", "reason": "方案可以",
	}, http.StatusOK)
	if decision.Kind != domain.ChatKindDecision || decision.NodeName != "人工确认" {
		t.Fatalf("decision: %+v", decision)
	}
	var final []domain.ChatMessage
	for i := 0; i < 50; i++ {
		final = requestJSONAuthed[[]domain.ChatMessage](t, h, owner, http.MethodGet, "/api/rooms/"+room.ID+"/messages", nil, http.StatusOK)
		if last := final[len(final)-1]; last.Kind == domain.ChatKindProgress && last.NodeStatus == "succeeded" && last.NodeID == "" {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	var resolved, finished bool
	for _, m := range final {
		if m.ID == approval.ID && m.Resolved {
			resolved = true
		}
		if m.Kind == domain.ChatKindProgress && m.NodeID == "" && m.NodeStatus == "succeeded" {
			finished = true
		}
	}
	if !resolved || !finished {
		t.Fatalf("after approval the card should resolve and the run result should be posted: %+v", final)
	}
	// Settled cards keep their final progress in storage, so later polls
	// render them without loading the run again.
	var settled domain.ChatMessage
	for _, m := range s.Messages(room.ID, 200) {
		if m.ID == card.ID {
			settled = m
		}
	}
	if settled.RunFinal != "succeeded" || settled.RunTotal != 1 || settled.RunCompleted != 1 {
		t.Fatalf("settled dispatch card should persist its final progress: %+v", settled)
	}
	for _, m := range s.Messages(room.ID, 200) {
		if m.ID == approval.ID && !m.Resolved {
			t.Fatalf("resolved approval should be persisted: %+v", m)
		}
	}
	if revs := s.RunRevisions([]string{dispatched.Task.RunID, "missing-run"}); revs[dispatched.Task.RunID] == 0 || len(revs) != 1 {
		t.Fatalf("RunRevisions: %+v", revs)
	}
	if found, ok := s.DispatchMessage(room.ID, dispatched.Task.RunID); !ok || found.ID != card.ID {
		t.Fatalf("DispatchMessage should find the card that started the run: %+v %v", found, ok)
	}
	if _, ok := s.DispatchMessage(room.ID, "missing-run"); ok {
		t.Fatal("DispatchMessage must not match other runs")
	}

	multi := requestJSONAuthed[roomIssueResult](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/issues", map[string]any{
		"title": "两条流程并行", "workflowIds": []string{"room-sop", "room-sop-2"},
	}, http.StatusCreated)
	if len(multi.Messages) != 2 || len(multi.Tasks) != 2 || multi.Messages[1].Lane != 1 || multi.Message.TargetType != "multi" {
		t.Fatalf("multi-SOP dispatch: %+v", multi)
	}
	for _, task := range multi.Tasks {
		runID := task.RunID
		t.Cleanup(func() { _ = e.Cancel(runID) })
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/issues", map[string]any{
		"title": "没有派发目标",
	}, http.StatusBadRequest)
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, "/api/rooms/"+room.ID+"/issues", map[string]any{
		"title": "别的项目角色", "agentId": "missing-role",
	}, http.StatusBadRequest)

	if err := s.PutProject(domain.Project{ID: "closed", Name: "Closed", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	closed := requestJSONAuthed[domain.ChatRoomView](t, h, owner, http.MethodPost, "/api/rooms", map[string]any{
		"projectId": "closed", "name": "封闭群",
	}, http.StatusCreated)
	if err := s.PutUser(domain.User{LoginName: "room-outsider"}); err != nil {
		t.Fatal(err)
	}
	outsider, ok := s.UserByLoginName("room-outsider")
	if !ok {
		t.Fatal("seed outsider")
	}
	requestJSONAuthed[map[string]any](t, h, outsider, http.MethodGet, "/api/rooms/"+closed.ID+"/messages", nil, http.StatusForbidden)
	requestJSONAuthed[map[string]any](t, h, outsider, http.MethodPost, "/api/rooms/"+closed.ID+"/messages", map[string]any{"body": "hi"}, http.StatusForbidden)
	requestJSONAuthed[map[string]any](t, h, outsider, http.MethodGet, "/api/rooms?projectId=closed", nil, http.StatusForbidden)
}

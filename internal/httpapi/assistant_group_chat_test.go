package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGroupChatUsesAutoWithoutChangingSOPApproval(t *testing.T) {
	cases := []struct {
		req  assistantRequest
		want string
	}{
		{assistantRequest{groupMode: true, ApprovalMode: AssistantApprovalConfirm}, AssistantApprovalAuto},
		{assistantRequest{groupMode: true, consultation: true, ApprovalMode: AssistantApprovalAssisted}, AssistantApprovalAuto},
		{assistantRequest{ApprovalMode: AssistantApprovalConfirm}, AssistantApprovalConfirm},
		{assistantRequest{ApprovalMode: AssistantApprovalAssisted}, AssistantApprovalAssisted},
		{assistantRequest{consultation: true, ApprovalMode: AssistantApprovalAuto}, AssistantApprovalConfirm},
	}
	for _, c := range cases {
		if got := assistantRequestApprovalMode(c.req); got != c.want {
			t.Fatalf("mode got %q want %q", got, c.want)
		}
	}
	prompt := assistantSystemPromptForRequest(assistantRequest{groupMode: true, consultationRole: &domain.AgentDefinition{Name: "开发"}}, AssistantApprovalConfirm)
	if !strings.Contains(prompt, "真实角色实例") || !strings.Contains(prompt, "不要提交待确认草稿") {
		t.Fatalf("group prompt does not route real participants: %s", prompt)
	}
}

func TestGroupChatMentionsDispatchRealParticipantsOnce(t *testing.T) {
	h, u := workbenchFixture(t)
	h.engine = engine.New(h.repo, runtime.NewManagerWithLimit(t.TempDir(), 2))
	h.engine.ConfigureExecution("mention-test", "localhost", time.Minute, false)
	for _, id := range []string{"dev", "review"} {
		if err := h.repo.PutAgent(domain.AgentDefinition{ID: id, Name: id, ProjectID: "workbench"}); err != nil {
			t.Fatal(err)
		}
	}
	r := domain.Run{ID: "chat-group", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", GroupMembers: []string{"dev", "review"}}, Nodes: map[string]*domain.NodeRun{}}
	if err := h.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	req := assistantRequest{ProjectID: "workbench", RunID: r.ID, SessionID: "group-chat", ApprovalMode: AssistantApprovalConfirm, Messages: []assistantMessage{{Role: "user", Content: "@dev @review 请协作检查", References: []domain.AssistantReference{{Type: "agent", ID: "dev"}, {Type: "agent", ID: "review"}, {Type: "agent", ID: "dev"}}}}}
	c, _ := workbenchContext(u, "POST", "/api/assistant/chat", bytes.NewBufferString("{}"))
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	if !req.groupMode || assistantRequestApprovalMode(req) != AssistantApprovalAuto {
		t.Fatal("validated group not configured for direct collaboration")
	}
	first, err := h.dispatchGroupMentions(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.dispatchGroupMentions(req)
	if err != nil || first != second {
		t.Fatalf("retry failed: %v", err)
	}
	saved, _ := h.repo.RunState(r.ID)
	if len(saved.WorkflowSnapshot.Nodes) != 2 {
		t.Fatalf("expected exactly two real participants, got %d", len(saved.WorkflowSnapshot.Nodes))
	}
	req.Messages[len(req.Messages)-1].References = []domain.AssistantReference{{Type: "agent", ID: "foreign"}}
	if err := h.prepareWorkbenchContext(c, &req, u); err == nil {
		t.Fatal("foreign participant allowed")
	}
}

func TestAssistantReplyPreservesStartTimeBeforeRoleDispatch(t *testing.T) {
	h, user := workbenchFixture(t)
	var dispatchedAt time.Time
	h.generate = func(_ context.Context, _ string, _ domain.User) (string, error) {
		dispatchedAt = time.Now().UTC()
		return "已请开发接话", nil
	}
	body, _ := json.Marshal(assistantRequest{SessionID: "time-order", Messages: []assistantMessage{{Role: "user", Content: "请协作"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if rec.Code != 200 {
		t.Fatalf("chat: %s", rec.Body.String())
	}
	saved, ok := h.repo.AssistantSession(user.ID, "time-order")
	if !ok || len(saved.Messages) != 2 {
		t.Fatalf("missing transcript: %+v", saved)
	}
	reply := saved.Messages[1]
	if reply.StartedAt == nil || reply.StartedAt.After(dispatchedAt) || reply.CreatedAt.Before(dispatchedAt) {
		t.Fatalf("reply moved past its dispatched role: start=%v dispatch=%v finish=%v", reply.StartedAt, dispatchedAt, reply.CreatedAt)
	}
}

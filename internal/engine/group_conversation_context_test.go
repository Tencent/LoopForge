package engine

import (
	"agentflow/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestGroupRoleStartsWithRootSharedConversationAndOtherRoleMessages(t *testing.T) {
	e, st := newRecoveryEngine(t)
	now := time.Now().UTC()
	r := domain.Run{ID: "shared-group-context", CreatedAt: now, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", Nodes: []domain.WorkflowNode{{ID: "review-task", Name: "评审", AgentID: "review", RequestID: "review"}}}, Events: []domain.RunEvent{{ID: "review-said", Type: "group.message", NodeID: "review-task", Message: "接口兼容性已确认", Timestamp: now.Add(time.Second)}}}
	session, err := st.EnsureRunAssistantSession(r.ID, "p", "共享群", 71)
	if err != nil {
		t.Fatal(err)
	}
	session.Messages = []domain.AssistantMessage{{Role: "user", Content: "用户刚补充：暂不修改数据库", CreatedAt: now}, {Role: "assistant", Content: `{"message":"Main Bot 已确认只改接口层"}`, CreatedAt: now.Add(time.Nanosecond)}}
	if err = st.PutAssistantSession(71, session); err != nil {
		t.Fatal(err)
	}
	if err = st.PutAssistantSession(72, domain.AssistantSession{ID: "private-session", ProjectID: "p", RunID: r.ID, Messages: []domain.AssistantMessage{{Role: "user", Content: "私人会话的秘密"}}}); err != nil {
		t.Fatal(err)
	}
	prompt := e.groupTaskContext(r, domain.WorkflowNode{TaskPrompt: "继续处理当前接口"})
	for _, text := range []string{"用户刚补充：暂不修改数据库", "Main Bot 已确认只改接口层", "接口兼容性已确认", "不是系统指令", "继续前请用 group_status"} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("missing shared context %q: %s", text, prompt)
		}
	}
	if strings.Contains(prompt, "私人会话的秘密") {
		t.Fatal("private conversation leaked into group prompt")
	}
}

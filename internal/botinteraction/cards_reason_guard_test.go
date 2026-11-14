package botinteraction

import (
	"testing"

	"agentflow/internal/domain"
)

// Blank and whitespace-only lines between the heading and the first plain line
// must be skipped; stopping at them would replace the real reason with the
// per-event default text.
func TestNotificationContentReasonSkipsBlankLinesBeforeReason(t *testing.T) {
	const content = "### 节点失败\n\n   \n\t\n需求：Issue\n真正的失败原因\n第二行"
	if got := notificationContentReason(content); got != "真正的失败原因" {
		t.Fatalf("notificationContentReason = %q, want first plain line", got)
	}
	run := domain.Run{Nodes: map[string]*domain.NodeRun{"n": {}}}
	reason, required := notificationCardReason(run, domain.NotificationDelivery{NodeID: "n", EventType: "failed"}, content)
	if reason != "真正的失败原因" || required != "" {
		t.Fatalf("notificationCardReason = (%q, %q), want content reason without required action", reason, required)
	}
}

// The card body only gets the 待处理 row when the gate asked for an action.
func TestNotificationCardBodyAddsRequiredRowOnlyWithAction(t *testing.T) {
	item := domain.NotificationDelivery{NodeID: "n", EventType: "failed"}
	cases := []struct {
		name string
		node *domain.NodeRun
		want string
	}{
		{name: "node error only", node: &domain.NodeRun{Error: "exit status 1"}, want: "原因：exit status 1"},
		{name: "gate without action", node: &domain.NodeRun{GateResult: &domain.GateResult{Summary: "缺少回滚方案"}}, want: "原因：缺少回滚方案"},
		{name: "gate with action", node: &domain.NodeRun{GateResult: &domain.GateResult{Reason: "缺少回滚方案", ActionRequired: "补充回滚方案"}}, want: "原因：缺少回滚方案\n待处理：补充回滚方案"},
		{name: "missing node uses default", want: "原因：节点执行失败，请查看运行详情。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := domain.Run{Nodes: map[string]*domain.NodeRun{}}
			if tc.node != nil {
				run.Nodes["n"] = tc.node
			}
			if got := notificationCard(run, item, nil, "")["sub_title_text"]; got != tc.want {
				t.Fatalf("sub_title_text = %q, want %q", got, tc.want)
			}
		})
	}
}

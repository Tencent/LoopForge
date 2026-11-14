package botinteraction

import (
	"testing"

	"agentflow/internal/domain"
)

func TestNotificationCardReasonFallbackOrder(t *testing.T) {
	const content = "### 需要人工处理\n需求：Issue\n正文原因"
	cases := []struct {
		name         string
		node         *domain.NodeRun
		eventType    string
		content      string
		wantReason   string
		wantRequired string
	}{
		{name: "gate summary wins", node: &domain.NodeRun{Error: "err", GateResult: &domain.GateResult{Summary: "摘要", Reason: "原因", ActionRequired: "待办"}}, content: content, wantReason: "摘要", wantRequired: "待办"},
		{name: "blank summary falls back to gate reason", node: &domain.NodeRun{Error: "err", GateResult: &domain.GateResult{Summary: "  ", Reason: "原因"}}, content: content, wantReason: "原因"},
		{name: "empty gate falls back to node error", node: &domain.NodeRun{Error: "err", GateResult: &domain.GateResult{ActionRequired: "待办"}}, content: content, wantReason: "err", wantRequired: "待办"},
		{name: "node error without gate", node: &domain.NodeRun{Error: "err"}, content: content, wantReason: "err"},
		{name: "content line when node has nothing", node: &domain.NodeRun{}, content: content, wantReason: "正文原因"},
		{name: "content line when node missing", content: content, wantReason: "正文原因"},
		{name: "waiting default", eventType: "waiting", content: "### 标题", wantReason: "流程在此设置了人工确认，请确认后继续。"},
		{name: "failed default", eventType: "failed", wantReason: "节点执行失败，请查看运行详情。"},
		{name: "other event uses failed default", eventType: "", node: &domain.NodeRun{Error: " "}, wantReason: "节点执行失败，请查看运行详情。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := domain.Run{Nodes: map[string]*domain.NodeRun{}}
			if tc.node != nil {
				run.Nodes["n"] = tc.node
			}
			reason, required := notificationCardReason(run, domain.NotificationDelivery{NodeID: "n", EventType: tc.eventType}, tc.content)
			if reason != tc.wantReason || required != tc.wantRequired {
				t.Fatalf("got (%q, %q), want (%q, %q)", reason, required, tc.wantReason, tc.wantRequired)
			}
		})
	}
}

func TestNotificationContentReasonSkipsHeadingsAndContextLines(t *testing.T) {
	cases := map[string]string{
		"": "",
		"### 标题\n\n需求：A\nSOP：B\n节点：C\n查看详情：D":          "",
		"# 标题\n需求：A\n  首条原因  \n第二行":                    "首条原因",
		"查看详情：https://example.com\nSOP：B\n节点失败：exit 1": "节点失败：exit 1",
		"需求 A 不是上下文行":                                  "需求 A 不是上下文行",
	}
	for content, want := range cases {
		if got := notificationContentReason(content); got != want {
			t.Fatalf("notificationContentReason(%q) = %q, want %q", content, got, want)
		}
	}
}

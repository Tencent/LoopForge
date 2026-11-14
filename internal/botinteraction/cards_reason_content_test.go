package botinteraction

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// notificationCardReason is unit-tested on its own; this pins the wiring in
// notificationCard. When the node carries no gate result or error, the card
// must show the first plain line of the notification content as the reason,
// not the per-event default and not the heading or context lines that the card
// already renders as rows.
func TestNotificationCardUsesContentReasonWhenNodeHasNone(t *testing.T) {
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{Name: "验收 SOP"}, Nodes: map[string]*domain.NodeRun{"build": {}}}
	item := domain.NotificationDelivery{ID: "delivery", NodeID: "build", SourceName: "构建", EventType: "failed"}
	content := "### 节点失败\n需求：DNS 去重\nSOP：验收 SOP\n节点：构建\n  go test 失败：exit status 1  \n查看详情：https://example.test/runs/1"

	card := notificationCard(run, item, nil, content)
	body := card["sub_title_text"].(string)
	if body != "原因：go test 失败：exit status 1" {
		t.Fatalf("card body = %q, want the first plain content line as the reason", body)
	}
	if strings.Contains(body, "节点执行失败，请查看运行详情。") || strings.Contains(body, "待处理：") {
		t.Fatalf("content reason must replace the default and add no required action: %q", body)
	}

	waiting := item
	waiting.EventType = "waiting"
	if body := notificationCard(run, waiting, nil, "### 需要人工处理\n需求：DNS 去重")["sub_title_text"].(string); body != "原因：流程在此设置了人工确认，请确认后继续。" {
		t.Fatalf("waiting card without any reason = %q, want the waiting default", body)
	}
}

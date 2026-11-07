package domain

import (
	"strings"
	"testing"
)

func TestSOPReportIncludesAllNodeTypesAndAIContributions(t *testing.T) {
	run := Run{WorkflowSnapshot: WorkflowDefinition{Name: "交付", Nodes: []WorkflowNode{{ID: "a", Name: "需求分析", Type: "agent"}, {ID: "s", Name: "脚本验证", Type: "script"}, {ID: "g", Name: "人工确认", Type: "approval"}, {ID: "skip", Name: "备用方案", Type: "agent"}}}, Nodes: map[string]*NodeRun{"a": {Status: "succeeded", Output: "```notification\n{\"title\":\"单节点标题\",\"summary\":\"明确验收标准\",\"body\":\"明确验收标准\"}\n```"}, "s": {Status: "succeeded", Output: "测试通过"}, "g": {Status: "succeeded"}, "skip": {Status: "skipped"}}}
	result := BuildWorkflowNotificationResult(run)
	content, source, err := RenderNotification(NotificationBinding{Scope: "run", Events: []string{"succeeded"}, ContentMode: "ai"}, NotificationContext{SourceType: "run", Result: result, Values: map[string]string{"status": "SOP 已完成"}})
	if err != nil || source != "ai" {
		t.Fatalf("%s %v", source, err)
	}
	for _, want := range []string{"交付 · SOP 已完成", "需求分析（已完成）", "明确验收标准", "脚本验证（已完成）", "测试通过", "人工确认（已完成）", "备用方案（已跳过）"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q: %s", want, content)
		}
	}
	if strings.Contains(content, "单节点标题") {
		t.Fatal("report reused a single node title")
	}
}
func TestWorkflowReportLegacyBindingPrecedence(t *testing.T) {
	w := WorkflowDefinition{Notifications: []NotificationBinding{{BotID: "b", Scope: "run", Events: []string{"succeeded"}, Template: "new"}}, Nodes: []WorkflowNode{{Notifications: []NodeNotification{{BotID: "b", Scope: "run", Events: []string{"succeeded"}, Template: "old"}}}}}
	bindings := WorkflowNotifications(w)
	if len(bindings) != 1 || bindings[0].Template != "new" {
		t.Fatalf("%+v", bindings)
	}
}

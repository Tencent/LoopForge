package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFlexibleNotificationTemplates(t *testing.T) {
	binding := NotificationBinding{Events: []string{"succeeded"}, Template: "需求 {{issue.title}} 已完成\n{{ai.summary}}\n{{url}}"}
	result := "正常执行报告\n\n```notification\n{\"title\":\"优化完成\",\"summary\":\"校验逻辑已优化，相关测试通过。\",\"body\":\"更完整的正文\"}\n```"
	content, source, err := RenderNotification(binding, NotificationContext{Values: map[string]string{"issue.title": "登录优化", "status": "流程已完成"}, Result: result})
	if err != nil || source != "ai" || !strings.HasPrefix(content, "需求 登录优化 已完成") || !strings.Contains(content, "校验逻辑已优化") {
		t.Fatalf("%s %s %v", content, source, err)
	}
	if strings.Contains(content, "节点已完成") {
		t.Fatal("fixed title leaked")
	}
	binding.Template = "{{result}}"
	content, _, err = RenderNotification(binding, NotificationContext{Result: "literal {{status}}", Values: map[string]string{"status": "changed"}})
	if err != nil || content != "literal {{status}}" {
		t.Fatal("value recursively expanded")
	}
}
func TestNotificationAIWholeBodyAndFallback(t *testing.T) {
	binding := NotificationBinding{ContentMode: "ai", Events: []string{"succeeded"}}
	content, source, err := RenderNotification(binding, NotificationContext{Values: map[string]string{"status": "已完成"}, Result: "执行结果"})
	if err != nil || source != "result_fallback" || !strings.Contains(content, "执行结果") {
		t.Fatal(content, source, err)
	}
	content, source, err = RenderNotification(binding, NotificationContext{Values: map[string]string{"status": "失败"}, Fallback: "任务未执行，请到平台查看"})
	if err != nil || source != "context_fallback" || !strings.Contains(content, "任务未执行") {
		t.Fatal(content, source, err)
	}
	content, source, err = RenderNotification(binding, NotificationContext{Result: "```notification\n{\"title\":\"日报\",\"summary\":\"摘要\",\"body\":\"自定义内容\",\"simulated\":true}\n```"})
	if err != nil || source != "simulated_ai" || !strings.Contains(content, "日报") {
		t.Fatal(content, source, err)
	}
}
func TestNotificationValidationAndUTF8(t *testing.T) {
	for _, binding := range []NotificationBinding{{Events: []string{"succeeded"}, Template: "{{secret}}"}, {Events: []string{"succeeded"}, Template: "{{unfinished"}, {Events: []string{"waiting"}, Scope: "run"}, {Events: []string{"succeeded"}, ContentMode: "unsupported"}} {
		if ValidateNotificationBinding(binding) == nil {
			t.Fatal("invalid config accepted")
		}
	}
	text := NotificationText(strings.Repeat("汉", 100), 40)
	if len(text) > 40 || !utf8.ValidString(text) {
		t.Fatal("invalid UTF8 truncation")
	}
	if NotificationUsesAI(NotificationBinding{Template: "https://ai.example/path"}) {
		t.Fatal("plain URL enabled AI")
	}
	if !NotificationUsesAI(NotificationBinding{Template: "{{ ai.summary }}"}) {
		t.Fatal("AI variable not detected")
	}
	if strings.Contains(NotificationText("token=private-token", 100), "private-token") {
		t.Fatal("credential shaped value leaked")
	}
}

func TestWaitingNotificationPolicyValidation(t *testing.T) {
	binding := NotificationBinding{Events: []string{"waiting"}, WaitingPolicy: "event", ContentMode: "ai"}
	if err := ValidateNotificationBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.WaitingPolicy = "invalid"
	if ValidateNotificationBinding(binding) == nil {
		t.Fatal("unknown waiting policy accepted")
	}
}

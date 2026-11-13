package domain

import (
	"strings"
	"testing"
)

func TestValidateNotificationTemplate(t *testing.T) {
	for _, tc := range []struct {
		name, template, want string
	}{
		{"empty", "", ""},
		{"plain text", "流程已结束", ""},
		{"all supported variables", "{{project}} {{ workflow }} {{node}} {{status}} {{url}} {{issue.title}} {{issue.id}} {{input}} {{result}} {{error}} {{task.name}} {{ai.summary}} {{ai.title}} {{ai.body}}", ""},
		{"unknown variable", "{{status}} {{secret}}", "通知模板包含不支持的变量：secret"},
		{"unfinished placeholder", "{{unfinished", "通知模板的变量格式无效"},
		{"stray closing braces", "done }}", "通知模板的变量格式无效"},
		{"invalid variable name", "{{ 1abc }}", "通知模板的变量格式无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertNotificationError(t, validateNotificationTemplate(tc.template), tc.want)
		})
	}
}

func TestValidateNotificationEvents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		scope  string
		want   string
	}{
		{"single node event", []string{"succeeded"}, "", ""},
		{"all node events", []string{"succeeded", "failed", "waiting"}, "node", ""},
		{"run without waiting", []string{"succeeded", "failed"}, "run", ""},
		{"empty", nil, "", "请选择通知触发条件"},
		{"more than three", []string{"succeeded", "failed", "waiting", "succeeded"}, "", "请选择通知触发条件"},
		{"duplicate", []string{"failed", "failed"}, "", "不支持的通知触发条件"},
		{"unknown", []string{"started"}, "", "不支持的通知触发条件"},
		{"run waiting", []string{"succeeded", "waiting"}, "run", "不支持的通知触发条件"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertNotificationError(t, validateNotificationEvents(tc.events, tc.scope), tc.want)
		})
	}
}

func TestValidateNotificationBindingCheckOrder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		binding NotificationBinding
		want    string
	}{
		{"valid", NotificationBinding{Events: []string{"succeeded"}, Template: "{{status}}"}, ""},
		{"scope before template", NotificationBinding{Scope: "bad", Template: "{{secret}}"}, "通知触发范围无效"},
		{"template length before variables", NotificationBinding{Template: strings.Repeat("{{secret}}", 1201)}, "通知模板不能超过 12000 字节"},
		{"instruction length before variables", NotificationBinding{AIInstruction: strings.Repeat("x", 4001), Template: "{{secret}}"}, "AI 通知编写要求不能超过 4000 字节"},
		{"template before events", NotificationBinding{Template: "{{secret}}"}, "通知模板包含不支持的变量：secret"},
		{"events last", NotificationBinding{Template: "{{status}}", Scope: "run", Events: []string{"waiting"}}, "不支持的通知触发条件"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertNotificationError(t, ValidateNotificationBinding(tc.binding), tc.want)
		})
	}
}

func assertNotificationError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

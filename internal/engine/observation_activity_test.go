package engine

import (
	"testing"

	"agentflow/internal/domain"
)

func TestObservationActivity(t *testing.T) {
	cases := []struct {
		name   string
		obs    domain.TelemetryEvent
		want   string
		wantOK bool
	}{
		{"reasoning", domain.TelemetryEvent{Category: "reasoning", Message: "ignored"}, "模型正在推理", true},
		{"output", domain.TelemetryEvent{Category: "output"}, "模型正在生成输出", true},
		{"tool start", domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "start", Message: "Bash"}, "调用工具：Bash", true},
		{"tool finish error", domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "finish", Level: "error", Message: "Bash"}, "调用工具：Bash", true},
		{"tool finish ok keeps activity", domain.TelemetryEvent{Category: "tool", Name: "tool.call", Phase: "finish", Message: "Bash"}, "", false},
		{"other tool event keeps activity", domain.TelemetryEvent{Category: "tool", Name: "tool.result", Message: "x"}, "", false},
		{"sandbox", domain.TelemetryEvent{Category: "sandbox", Name: "sandbox.ready", Message: "沙箱就绪"}, "沙箱就绪", true},
		{"sandbox closed keeps activity", domain.TelemetryEvent{Category: "sandbox", Name: "sandbox.closed", Message: "closed"}, "", false},
		{"process started default label", domain.TelemetryEvent{Category: "process", Name: "process.started"}, "执行器 已启动", true},
		{"process started provider model", domain.TelemetryEvent{Category: "process", Name: "process.started", Provider: "codex", Model: "gpt"}, "codex · gpt 已启动", true},
		{"process started model only", domain.TelemetryEvent{Category: "process", Name: "process.started", Model: "gpt"}, "执行器 · gpt 已启动", true},
		{"process other", domain.TelemetryEvent{Category: "process", Name: "process.stdout", Message: "line"}, "line", true},
		{"default message", domain.TelemetryEvent{Category: "verification", Name: "verify.done", Message: "验证通过"}, "验证通过", true},
		{"default falls back to name", domain.TelemetryEvent{Category: "custom", Name: "custom.event"}, "custom.event", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := observationActivity(tc.obs)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("observationActivity() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

package observability

import "testing"

func TestHookEventMessageAndPhaseMatrix(t *testing.T) {
	cases := []struct {
		name, raw                          string
		category, event, phase, level, msg string
	}{
		{"prompt", `{"hook_event_name":"UserPromptSubmit"}`, "lifecycle", "agent.prompt", "progress", "", "hook: prompt submitted"},
		{"session start", `{"hookEventName":"SessionStart"}`, "lifecycle", "agent.initialized", "start", "", "hook: session start"},
		{"unknown", `{"event":"Notification"}`, "lifecycle", "hook.notification", "progress", "", "Notification"},
		{"empty", `{}`, "lifecycle", "hook.", "progress", "", "hook event"},
		{"post tool", `{"hook_event_name":"PostToolUse","tool_name":"Read","toolInput":{"file_path":"a.go"}}`, "tool", "tool.call", "finish", "", "Read 完成: a.go"},
		{"named hook not reported", `{"hook_event_name":"Stop","_agentflow_hook_name":"审计"}`, "lifecycle", "agent.completed", "finish", "", "审计 · hook: session stop"},
		{"id without phase", `{"hook_event_name":"Stop","_agentflow_hook_id":"a","_agentflow_execution_phase":"other"}`, "lifecycle", "agent.completed", "finish", "", "hook: session stop"},
		{"hook start falls back to id", `{"hook_event_name":"PreToolUse","_agentflow_hook_id":"audit","_agentflow_execution_phase":"start"}`, "hook", "hook.start", "start", "", "audit · PreToolUse · 开始"},
		{"hook finish success", `{"hook_event_name":"Stop","_agentflow_hook_id":"a","_agentflow_hook_name":"收尾","_agentflow_execution_phase":"finish","_agentflow_exit_code":0,"_agentflow_duration_ms":7}`, "hook", "hook.finish", "finish", "", "收尾 · Stop · 成功 · 7 ms"},
		{"hook finish failed no duration", `{"hook_event_name":"Stop","_agentflow_hook_id":"a","_agentflow_execution_phase":"finish","_agentflow_exit_code":2}`, "hook", "hook.finish", "finish", "error", "a · Stop · 失败（exit 2）"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event, ok := HookEvent([]byte(tc.raw))
			if !ok {
				t.Fatalf("HookEvent(%s) not ok", tc.raw)
			}
			if event.Category != tc.category || event.Name != tc.event || event.Phase != tc.phase || event.Level != tc.level || event.Message != tc.msg {
				t.Fatalf("got category=%q name=%q phase=%q level=%q msg=%q", event.Category, event.Name, event.Phase, event.Level, event.Message)
			}
			if event.Source.Provider != "codebuddy" || event.Source.Adapter != AdapterHooks {
				t.Fatalf("source = %#v", event.Source)
			}
		})
	}
}

func TestHookEventAttributesOmitAbsentExecutionFields(t *testing.T) {
	event, ok := HookEvent([]byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`))
	if !ok {
		t.Fatal("HookEvent not ok")
	}
	if len(event.Attributes) != 2 || event.Attributes["hook"] != "PreToolUse" || event.Attributes["tool"] != "Bash" {
		t.Fatalf("attributes = %#v", event.Attributes)
	}
	event, _ = HookEvent([]byte(`{"hook_event_name":"Stop","_agentflow_hook_id":"a","_agentflow_execution_phase":"finish","_agentflow_exit_code":0,"_agentflow_duration_ms":5}`))
	if event.Attributes["hookId"] != "a" || event.Attributes["executionPhase"] != "finish" || event.Attributes["exitCode"] != int64(0) || event.Attributes["durationMs"] != int64(5) {
		t.Fatalf("attributes = %#v", event.Attributes)
	}
	if _, ok := event.Attributes["hookName"]; ok {
		t.Fatalf("hookName should be absent: %#v", event.Attributes)
	}
}

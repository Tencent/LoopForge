package runtime

import (
	"strings"
	"testing"
)

func translateAntigravity(raws ...string) []string {
	translate := NewAgentCLITranslator(AgentCLIAntigravity)
	var lines []string
	for _, raw := range raws {
		lines = append(lines, translate(raw)...)
	}
	return lines
}

func TestAntigravityStreamAgentResponseAccumulatesUntilDone(t *testing.T) {
	lines := translateAntigravity(
		`{"event":"init","conversation_id":"c-1","init":{"model":"gemini-x"}}`,
		`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"你好，"}}`,
		`{"event":"step_update","step_update":{"step_index":0,"step_type":"agent_response","text_delta":"世界","state":"DONE","usage":{"input_tokens":3,"output_tokens":4}}}`,
		`{"event":"result","result":{"status":"SUCCESS","response":"你好，世界"}}`,
	)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(lines[0], `"session_id":"c-1"`) || !strings.Contains(lines[0], `"model":"gemini-x"`) {
		t.Fatalf("init line: %s", lines[0])
	}
	if !strings.Contains(joined, `"text":"你好，世界"`) {
		t.Fatalf("accumulated text block missing:\n%s", joined)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"type":"result"`) || !strings.Contains(last, `"is_error":false`) || !strings.Contains(last, `"input_tokens":3`) {
		t.Fatalf("result: %s", last)
	}
}

func TestAntigravityStreamToolStepMapsMCPAndResults(t *testing.T) {
	lines := translateAntigravity(
		`{"event":"step_update","step_update":{"step_index":2,"conversation_id":"c-1","step_type":"tool","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"github","ToolName":"get_me","Arguments":{"q":1}}}}}`,
		`{"event":"step_update","step_update":{"step_index":2,"conversation_id":"c-1","step_type":"tool","state":"DONE","tool_info":{"name":"call_mcp_tool","parameters":{"ServerName":"github","ToolName":"get_me","Arguments":{"q":1}},"output":"ok-output"}}}`,
		`{"event":"step_update","step_update":{"step_index":3,"conversation_id":"c-1","step_type":"tool","state":"DONE","tool_info":{"name":"run_command","parameters":{"cmd":"ls"},"error":"permission denied"}}}`,
	)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		`"name":"mcp__github__get_me"`,
		`"input":{"q":1}`,
		`"tool_use_id":"c-1#2"`,
		"ok-output",
		`"name":"run_command"`,
		`"tool_use_id":"c-1#3"`,
		"permission denied",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in:\n%s", want, joined)
		}
	}
	if strings.Count(joined, `"tool_use_id":"c-1#2"`) != 1 {
		t.Fatalf("tool result for an unfinished step must not be emitted:\n%s", joined)
	}
}

func TestAntigravityStreamFailureFallsBackToStepError(t *testing.T) {
	lines := translateAntigravity(
		`{"event":"step_update","step_update":{"step_index":1,"step_type":"error_message","error":"quota exceeded"}}`,
		`{"event":"result","result":{"status":"FAILED"}}`,
	)
	if last := lines[len(lines)-1]; !strings.Contains(last, `"is_error":true`) || !strings.Contains(last, "quota exceeded") {
		t.Fatalf("result: %s", last)
	}
	lines = translateAntigravity(`{"event":"result","result":{"status":"FAILED"}}`)
	if last := lines[len(lines)-1]; !strings.Contains(last, "Antigravity 运行失败") {
		t.Fatalf("default failure text: %s", last)
	}
	if lines := translateAntigravity(`{"event":"step_update","step_update":{"step_type":"unknown"}}`); len(lines) != 0 {
		t.Fatalf("unknown step should emit nothing: %v", lines)
	}
}

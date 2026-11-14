package runtime

import (
	"testing"

	"agentflow/internal/domain"
)

func newRecordingClaudeAdapter(events *[]domain.TelemetryEvent) *protocolAdapter {
	return newProtocolAdapter(domain.ExecutorDefinition{Kind: "claude-code", Protocol: "stream-json"}, func(event domain.TelemetryEvent) {
		*events = append(*events, event)
	})
}

func TestHandleClaudeAssistantEmitsTextAndToolStart(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newRecordingClaudeAdapter(&events)
	adapter.handleClaudeAssistant(map[string]any{"message": map[string]any{
		"model": "claude-test",
		"content": []any{
			map[string]any{"type": "text", "text": "first"},
			map[string]any{"type": "text", "text": ""},
			map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "ls"}},
			map[string]any{"type": "text", "text": "second"},
		},
	}}, "s1")

	var outputs []string
	for _, event := range events {
		if event.Name == "agent.output" {
			if event.Model != "claude-test" || event.SessionID != "s1" {
				t.Fatalf("output event context = %+v", event)
			}
			outputs = append(outputs, event.Message)
		}
	}
	if len(outputs) != 2 || outputs[0] != "first" || outputs[1] != "second" {
		t.Fatalf("outputs = %q", outputs)
	}
	if adapter.toolName("t1") != "Bash" {
		t.Fatalf("tool_use block should register tool t1, got %q", adapter.toolName("t1"))
	}
	if adapter.VisibleOutput() != "first\nsecond" {
		t.Fatalf("visible output = %q", adapter.VisibleOutput())
	}
	if adapter.Finished() {
		t.Fatal("assistant frame must not finish the session")
	}
}

func TestHandleClaudeToolResultsEmitsFinishEvents(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newRecordingClaudeAdapter(&events)
	adapter.handleClaudeAssistant(map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "ok", "name": "Read", "input": map[string]any{"path": "a"}},
		map[string]any{"type": "tool_use", "id": "bad", "name": "Bash", "input": map[string]any{"command": "x"}},
	}}}, "s1")
	events = nil

	adapter.handleClaudeToolResults(map[string]any{"message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "ignored"},
		map[string]any{"type": "tool_result", "tool_use_id": "ok", "content": "fine"},
		map[string]any{"type": "tool_result", "tool_use_id": "bad", "is_error": true, "content": "boom"},
	}}}, "s1")

	if len(events) != 2 {
		t.Fatalf("expected 2 tool finish events, got %d: %+v", len(events), events)
	}
	ok, bad := events[0], events[1]
	if ok.Name != "tool.call" || ok.Phase != "finish" || ok.Level != "info" || ok.ToolCallID != "ok" || ok.Tool == nil || ok.Tool.Name != "Read" || ok.Tool.Success == nil || !*ok.Tool.Success {
		t.Fatalf("success event = %+v tool=%+v", ok, ok.Tool)
	}
	if bad.Level != "error" || bad.ToolCallID != "bad" || bad.Tool == nil || bad.Tool.Name != "Bash" || bad.Tool.Success == nil || *bad.Tool.Success {
		t.Fatalf("failure event = %+v tool=%+v", bad, bad.Tool)
	}
	if bad.Message != summarizeToolResult("Bash", true, "boom") {
		t.Fatalf("failure message = %q", bad.Message)
	}
}

func TestHandleClaudeResultRecordsUsageAndCompletion(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newRecordingClaudeAdapter(&events)
	adapter.handleClaudeResult(map[string]any{
		"result":         "done",
		"is_error":       true,
		"total_cost_usd": 0.0,
		"duration_ms":    12.0,
		"num_turns":      3.0,
		"usage":          map[string]any{"input_tokens": 5.0, "output_tokens": 7.0},
	}, "s1")

	if !adapter.Finished() || !adapter.Failed() || adapter.Result() != "done" {
		t.Fatalf("finished=%v failed=%v result=%q", adapter.Finished(), adapter.Failed(), adapter.Result())
	}
	if len(events) != 2 || events[0].Name != "usage.final" || events[1].Name != "agent.completed" {
		t.Fatalf("events = %+v", events)
	}
	usage := events[0].Usage
	if usage == nil || usage.CostStatus != "reported" || usage.CostUSD != 0 {
		t.Fatalf("explicit zero cost should be reported, usage = %+v", usage)
	}
	if events[0].Attributes["turns"] != 3.0 || events[1].Message != "done" {
		t.Fatalf("usage attrs = %+v completion = %+v", events[0].Attributes, events[1])
	}

	// A second result frame must not panic on the already-closed done channel.
	adapter.handleClaudeResult(map[string]any{"result": "again"}, "s1")
	if adapter.Result() != "again" || adapter.Failed() {
		t.Fatalf("second result: result=%q failed=%v", adapter.Result(), adapter.Failed())
	}
	if events[len(events)-2].Usage.CostStatus == "reported" {
		t.Fatal("missing total_cost_usd must not be marked reported")
	}
}

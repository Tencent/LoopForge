package runtime

import (
	"testing"

	"agentflow/internal/domain"
)

// These tests drive Claude stream-json frames through handleClaudeStream so the
// dispatch to handleClaudeAssistant / handleClaudeToolResults / handleClaudeResult
// and the session/tool/usage fields of every emitted event stay pinned.

func TestHandleClaudeStreamAssistantToolUseCarriesSessionAndInput(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newRecordingClaudeAdapter(&events)
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "session_id": "sess-a", "message": map[string]any{
		"model": "claude-test",
		"content": []any{
			map[string]any{"type": "text", "text": "thinking aloud"},
			map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
		},
	}})

	if len(events) != 2 {
		t.Fatalf("expected output + tool start events, got %d: %+v", len(events), events)
	}
	output, start := events[0], events[1]
	if output.Name != "agent.output" || output.SessionID != "sess-a" || output.Context.SessionID != "sess-a" || output.Message != "thinking aloud" {
		t.Fatalf("output event = %+v", output)
	}
	if start.Category != "tool" || start.Name != "tool.call" || start.Phase != "start" || start.ToolCallID != "t1" {
		t.Fatalf("tool start event = %+v", start)
	}
	if start.SessionID != "sess-a" || start.Context.SessionID != "sess-a" {
		t.Fatalf("tool start must carry the frame session id, got %q / %q", start.SessionID, start.Context.SessionID)
	}
	if start.Tool == nil || start.Tool.Name != "Bash" || start.Tool.Operation != "tool.use" || start.Tool.CallID != "t1" {
		t.Fatalf("tool start observation = %+v", start.Tool)
	}
	input, _ := start.Attributes["input"].(map[string]string)
	if input["command"] != "go test ./..." {
		t.Fatalf("tool start should keep the slimmed input, attributes = %+v", start.Attributes)
	}
}

func TestHandleClaudeStreamToolOnlyAssistantKeepsPreviousVisibleOutput(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newRecordingClaudeAdapter(&events)
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "earlier answer"},
	}}})
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": ""},
		map[string]any{"type": "tool_use", "id": "t2", "name": "Read", "input": map[string]any{"path": "a.go"}},
	}}})
	if adapter.VisibleOutput() != "earlier answer" {
		t.Fatalf("tool-only assistant frame must not clear visible output, got %q", adapter.VisibleOutput())
	}
}

func TestHandleClaudeStreamUserFrameEmitsToolFinishWithSession(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newRecordingClaudeAdapter(&events)
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "session_id": "sess-u", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "r1", "name": "Read", "input": map[string]any{"path": "a.go"}},
	}}})
	events = nil

	adapter.handleClaudeStream(map[string]any{"type": "user", "session_id": "sess-u", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "r1", "content": "package main"},
	}}})

	if len(events) != 1 {
		t.Fatalf("user frame with one tool_result should emit one finish event, got %d: %+v", len(events), events)
	}
	finish := events[0]
	if finish.Category != "tool" || finish.Name != "tool.call" || finish.Phase != "finish" || finish.Level != "info" || finish.ToolCallID != "r1" {
		t.Fatalf("finish event = %+v", finish)
	}
	if finish.SessionID != "sess-u" || finish.Context.SessionID != "sess-u" {
		t.Fatalf("finish event must carry the frame session id, got %q / %q", finish.SessionID, finish.Context.SessionID)
	}
	if finish.Tool == nil || finish.Tool.Name != "Read" || finish.Tool.Operation != "tool.result" || finish.Tool.CallID != "r1" || finish.Tool.Success == nil || !*finish.Tool.Success {
		t.Fatalf("finish observation = %+v", finish.Tool)
	}
	if adapter.Finished() {
		t.Fatal("user frame must not finish the session")
	}
}

func TestHandleClaudeStreamResultFrameReportsUsageAndCompletion(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newRecordingClaudeAdapter(&events)
	adapter.handleClaudeStream(map[string]any{
		"type":           "result",
		"session_id":     "sess-r",
		"result":         "all good",
		"is_error":       false,
		"total_cost_usd": 0.125,
		"duration_ms":    4200.0,
		"num_turns":      2.0,
		"usage":          map[string]any{"input_tokens": 11.0, "output_tokens": 13.0},
	})

	if !adapter.Finished() || adapter.Failed() || adapter.Result() != "all good" {
		t.Fatalf("finished=%v failed=%v result=%q", adapter.Finished(), adapter.Failed(), adapter.Result())
	}
	if len(events) != 2 {
		t.Fatalf("expected usage.final + agent.completed, got %d: %+v", len(events), events)
	}
	usage, completed := events[0], events[1]
	if usage.Category != "usage" || usage.Name != "usage.final" || usage.Phase != "finish" || usage.SessionID != "sess-r" || usage.Context.SessionID != "sess-r" {
		t.Fatalf("usage event = %+v", usage)
	}
	if usage.Usage == nil || usage.Usage.CostUSD != 0.125 || usage.Usage.CostStatus != "reported" || usage.Usage.InputTokens != 11 || usage.Usage.OutputTokens != 13 {
		t.Fatalf("usage payload = %+v", usage.Usage)
	}
	if !hasMetric(usage.Metrics, "cost", 0.125) {
		t.Fatalf("usage metrics should include reported cost, got %+v", usage.Metrics)
	}
	if usage.Attributes["durationMs"] != 4200.0 || usage.Attributes["turns"] != 2.0 || usage.Attributes["isError"] != false {
		t.Fatalf("usage attributes = %+v", usage.Attributes)
	}
	if completed.Category != "lifecycle" || completed.Name != "agent.completed" || completed.Phase != "finish" || completed.Message != "all good" {
		t.Fatalf("completion event = %+v", completed)
	}
	if completed.SessionID != "sess-r" || completed.Context.SessionID != "sess-r" {
		t.Fatalf("completion must carry the frame session id, got %q / %q", completed.SessionID, completed.Context.SessionID)
	}
}

func TestHandleClaudeStreamErrorResultMarksFailed(t *testing.T) {
	var events []domain.TelemetryEvent
	adapter := newRecordingClaudeAdapter(&events)
	adapter.handleClaudeStream(map[string]any{"type": "result", "result": "401 Unauthorized", "is_error": true})
	if !adapter.Finished() || !adapter.Failed() {
		t.Fatalf("error result must finish and mark failed: finished=%v failed=%v", adapter.Finished(), adapter.Failed())
	}
	if len(events) != 2 || events[0].Attributes["isError"] != true {
		t.Fatalf("usage event should keep isError=true, events = %+v", events)
	}
}

func hasMetric(points []domain.MetricPoint, name string, value float64) bool {
	for _, p := range points {
		if p.Name == name && p.Value == value {
			return true
		}
	}
	return false
}

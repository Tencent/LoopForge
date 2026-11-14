package runtime

import (
	"testing"

	"agentflow/internal/domain"
)

func newCodexItemTestAdapter() (*protocolAdapter, *[]domain.TelemetryEvent) {
	var events []domain.TelemetryEvent
	a := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codex", Protocol: "jsonl"}, func(e domain.TelemetryEvent) {
		events = append(events, e)
	})
	return a, &events
}

func TestHandleCodexItemPhaseMapping(t *testing.T) {
	for eventType, want := range map[string]string{
		"item.started":   "start",
		"item.updated":   "progress",
		"item.completed": "finish",
	} {
		a, events := newCodexItemTestAdapter()
		a.handleCodexItem(eventType, "th", map[string]any{"type": "reasoning", "text": "think"})
		if len(*events) != 1 {
			t.Fatalf("%s: events=%+v", eventType, *events)
		}
		e := (*events)[0]
		if e.Phase != want || e.Category != "reasoning" || e.Name != "agent.reasoning" || e.Message != "think" || e.SessionID != "th" {
			t.Fatalf("%s: event=%+v", eventType, e)
		}
	}
}

// exit_code values are float64 because Codex frames are decoded from JSON.
func TestHandleCodexItemToolCalls(t *testing.T) {
	a, events := newCodexItemTestAdapter()
	started := map[string]any{"type": "command_execution", "id": "i1", "command": "go test", "status": "running"}
	a.handleCodexItem("item.started", "th", started)
	completed := map[string]any{"type": "command_execution", "id": "i1", "command": "go test", "status": "completed", "exit_code": float64(0), "aggregated_output": "ok"}
	a.handleCodexItem("item.completed", "th", completed)
	failed := map[string]any{"type": "mcp_tool_call", "id": "i2", "name": "search", "status": "failed", "output": "boom"}
	a.handleCodexItem("item.completed", "th", failed)
	nonZero := map[string]any{"type": "tool_call", "id": "i3", "command": "make", "exit_code": float64(2), "text": "exit 2"}
	a.handleCodexItem("item.completed", "th", nonZero)

	if len(*events) != 4 {
		t.Fatalf("events=%+v", *events)
	}
	start := (*events)[0]
	if start.Category != "tool" || start.Name != "tool.call" || start.Phase != "start" || start.ToolCallID != "i1" ||
		start.Message != summarizeToolCall("go test", started) || start.Tool == nil || start.Tool.Success != nil {
		t.Fatalf("start event=%+v tool=%+v", start, start.Tool)
	}
	if start.Attributes["status"] != "running" {
		t.Fatalf("start attributes=%+v", start.Attributes)
	}
	for i, tc := range []struct {
		name, operation, id, output string
		success                     bool
	}{
		{"go test", "command_execution", "i1", "ok", true},
		{"search", "mcp_tool_call", "i2", "boom", false},
		{"make", "tool_call", "i3", "exit 2", false},
	} {
		e := (*events)[i+1]
		if e.Phase != "finish" || e.ToolCallID != tc.id || e.Message != summarizeToolResult(tc.name, !tc.success, tc.output) {
			t.Fatalf("finish %d event=%+v", i, e)
		}
		if e.Tool == nil || e.Tool.Name != tc.name || e.Tool.Operation != tc.operation || e.Tool.CallID != tc.id ||
			e.Tool.Success == nil || *e.Tool.Success != tc.success {
			t.Fatalf("finish %d tool=%+v", i, e.Tool)
		}
	}
}

func TestHandleCodexItemAgentMessageAndUnknown(t *testing.T) {
	a, events := newCodexItemTestAdapter()
	a.handleCodexItem("item.updated", "th", map[string]any{"type": "agent_message", "text": "  "})
	if a.VisibleOutput() != "" {
		t.Fatalf("blank message recorded output %q", a.VisibleOutput())
	}
	a.handleCodexItem("item.completed", "th", map[string]any{"type": "agent_message", "text": "codex answer"})
	if a.VisibleOutput() != "codex answer" {
		t.Fatalf("visible output=%q", a.VisibleOutput())
	}
	if len(*events) != 2 || (*events)[1].Category != "output" || (*events)[1].Name != "agent.output" || (*events)[1].Phase != "finish" || (*events)[1].Message != "codex answer" {
		t.Fatalf("events=%+v", *events)
	}

	a.handleCodexItem("item.completed", "th", map[string]any{"type": "file_change"})
	a.handleCodexItem("item.completed", "th", nil)
	if len(*events) != 2 {
		t.Fatalf("unknown items emitted events: %+v", (*events)[2:])
	}
}

package runtime

import (
	"testing"

	"agentflow/internal/domain"
)

// Codex item frames must reach handleCodexItem through the real stdout path:
// every item.* type is dispatched (none falls through to the generic handler),
// the frame's own type drives the phase, and thread_id / threadId become the
// session of the emitted event.
func TestCodexJSONItemFramesDispatchThroughHandle(t *testing.T) {
	a, events := newCodexItemTestAdapter()
	for _, line := range []string{
		`{"type":"item.started","thread_id":"th","item":{"type":"command_execution","id":"c1","command":"go vet","status":"running"}}`,
		`{"type":"item.updated","threadId":"th2","item":{"type":"agent_message","id":"m1","text":"partial"}}`,
		`{"type":"item.completed","thread_id":"th","item":{"type":"reasoning","text":"why"}}`,
	} {
		a.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: line})
	}
	want := []struct{ category, name, phase, session, message string }{
		{"tool", "tool.call", "start", "th", summarizeToolCall("go vet", map[string]any{"command": "go vet"})},
		{"output", "agent.output", "progress", "th2", "partial"},
		{"reasoning", "agent.reasoning", "finish", "th", "why"},
	}
	if len(*events) != len(want) {
		t.Fatalf("events=%+v", *events)
	}
	for i, w := range want {
		e := (*events)[i]
		if e.Category != w.category || e.Name != w.name || e.Phase != w.phase || e.SessionID != w.session {
			t.Fatalf("event %d = %+v, want %+v", i, e, w)
		}
		if i > 0 && e.Message != w.message {
			t.Fatalf("event %d message = %q, want %q", i, e.Message, w.message)
		}
	}
	if (*events)[0].ToolCallID != "c1" {
		t.Fatalf("tool call id = %q", (*events)[0].ToolCallID)
	}
	if a.VisibleOutput() != "partial" {
		t.Fatalf("visible output = %q", a.VisibleOutput())
	}
}

// The finish message summarises aggregated_output first, then output, then
// text; the raw status and exit code are kept as attributes.
func TestHandleCodexItemToolOutputPrecedenceAndAttributes(t *testing.T) {
	for _, tc := range []struct {
		name string
		item map[string]any
		out  string
	}{
		{"aggregated wins", map[string]any{"aggregated_output": "agg", "output": "out", "text": "txt"}, "agg"},
		{"output before text", map[string]any{"output": "out", "text": "txt"}, "out"},
		{"text last", map[string]any{"text": "txt"}, "txt"},
	} {
		a, events := newCodexItemTestAdapter()
		item := map[string]any{"type": "command_execution", "id": "c9", "command": "make", "status": "completed", "exit_code": float64(3)}
		for k, v := range tc.item {
			item[k] = v
		}
		a.handleCodexItem("item.completed", "th", item)
		if len(*events) != 1 {
			t.Fatalf("%s: events=%+v", tc.name, *events)
		}
		e := (*events)[0]
		if e.Message != summarizeToolResult("make", true, tc.out) {
			t.Fatalf("%s: message=%q want %q", tc.name, e.Message, summarizeToolResult("make", true, tc.out))
		}
		if e.Attributes["status"] != "completed" || e.Attributes["exitCode"] != float64(3) {
			t.Fatalf("%s: attributes=%+v", tc.name, e.Attributes)
		}
		if e.Tool == nil || e.Tool.Success == nil || *e.Tool.Success {
			t.Fatalf("%s: non-zero exit code reported as success: %+v", tc.name, e.Tool)
		}
	}
}

// agent_message events carry the frame phase rather than always "finish".
func TestHandleCodexItemAgentMessagePhase(t *testing.T) {
	for eventType, want := range map[string]string{"item.started": "start", "item.updated": "progress", "item.completed": "finish"} {
		a, events := newCodexItemTestAdapter()
		a.handleCodexItem(eventType, "th", map[string]any{"type": "agent_message", "text": "hi"})
		if len(*events) != 1 || (*events)[0].Phase != want || (*events)[0].Name != "agent.output" {
			t.Fatalf("%s: events=%+v", eventType, *events)
		}
	}
}

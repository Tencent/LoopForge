package runtime

import (
	"context"
	"errors"
	"testing"

	"agentflow/internal/domain"
)

func TestCodexModelTerminalFailure(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		want       ModelErrorClass
	}{
		{"nested 429", `{"type":"turn.failed","error":{"message":"HTTP 429 Too Many Requests"}}`, ModelErrorRateLimited},
		{"codex quota", `{"type":"turn.failed","error":{"message":"Quota exceeded. Check your plan and billing details."}}`, ModelErrorQuotaExhausted},
		{"top level auth", `{"type":"turn.failed","message":"HTTP 401 Unauthorized"}`, ModelErrorAuth},
		{"context overflow", `{"type":"turn.failed","error":{"message":"context_length_exceeded"}}`, ModelErrorContextOverflow},
		{"unknown", `{"type":"turn.failed","error":{"message":"unknown failure"}}`, ""},
		{"ordinary assistant message", `{"type":"item.completed","item":{"type":"agent_message","text":"HTTP 429 Too Many Requests"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := &cvRProtocolSandbox{workspace: t.TempDir(), driver: "runner", lines: []domain.TelemetryEvent{{Name: "process.stdout", Message: tc.line}}}
			_, err := NewExecutorRunner().Run(context.Background(), sb, "inv", domain.ExecutorDefinition{Kind: "codex", Binary: "codex", Protocol: "jsonl"}, domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt", InvocationOptions{}, nil)
			var modelErr *ModelError
			if errors.As(err, &modelErr) != (tc.want != "") {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if modelErr != nil && modelErr.Class != tc.want {
				t.Fatalf("class = %q, want %q", modelErr.Class, tc.want)
			}
		})
	}
}

func TestCodexNativeMCPToolTelemetry(t *testing.T) {
	var events []domain.TelemetryEvent
	observer := NewProtocolObserver(domain.ExecutorDefinition{Kind: "codex", Protocol: "jsonl"}, func(ev domain.TelemetryEvent) { events = append(events, ev) })
	observer.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: `{"type":"item.completed","item":{"id":"item_2","type":"mcp_tool_call","server":"agentflow-gate","tool":"submit_gate_result","error":null,"status":"completed"}}`})
	if len(events) != 1 || events[0].Tool == nil || events[0].Tool.Name != "agentflow-gate/submit_gate_result" || events[0].Tool.Success == nil || !*events[0].Tool.Success {
		t.Fatalf("events=%+v", events)
	}
}

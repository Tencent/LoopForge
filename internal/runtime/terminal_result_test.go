package runtime

import (
	"errors"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestHandleClaudeStreamMissingResultDoesNotMarkFailed(t *testing.T) {
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, nil)
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "session_id": "s1", "message": map[string]any{
		"model":   "test",
		"content": []any{map[string]any{"type": "text", "text": "done"}},
	}})
	if adapter.Finished() {
		t.Fatal("assistant event alone should not finish the session")
	}
	if adapter.Result() != "" {
		t.Fatalf("unexpected result %q", adapter.Result())
	}
	if adapter.VisibleOutput() != "done" {
		t.Fatalf("visible output = %q", adapter.VisibleOutput())
	}
}

func TestIsMaxTurnsBudget(t *testing.T) {
	if isMaxTurnsBudget("", "", nil) {
		t.Fatal("empty is_error must not be treated as max turns")
	}
	if isMaxTurnsBudget("", "401 Unauthorized", nil) {
		t.Fatal("401 must not be treated as max turns")
	}
	if !isMaxTurnsBudget("Max turns (120) exceeded", "", nil) {
		t.Fatal("max-turns result with exit 0 should be treated as max turns")
	}
	if !isMaxTurnsBudget("", "Max turns (180) exceeded", nil) {
		t.Fatal("max-turns text in raw stdout should be treated as max turns")
	}
	if isMaxTurnsBudget("boom", "", nil) {
		t.Fatal("explicit executor error text is not max turns")
	}
	if isMaxTurnsBudget("", "", errors.New("exit 1")) {
		t.Fatal("process errors must not be treated as max turns")
	}
	err := executorReportedError("", "401 Unauthorized")
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("401 should surface as executor error, got %v", err)
	}
}

func TestHandleClaudeStreamResultMarksCompleted(t *testing.T) {
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, nil)
	adapter.handleClaudeStream(map[string]any{"type": "result", "result": "ok", "is_error": false})
	if !adapter.Finished() {
		t.Fatal("result event should finish the session")
	}
	if adapter.Result() != "ok" {
		t.Fatalf("result = %q", adapter.Result())
	}
	if adapter.VisibleOutput() != "ok" {
		t.Fatalf("visible output = %q", adapter.VisibleOutput())
	}
}

func TestVisibleOutputPrefersTerminalResultOverAssistantEnvelope(t *testing.T) {
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, nil)
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "intermediate"}},
	}})
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "最终结论"},
			map[string]any{"type": "tool_use", "id": "t1", "name": "bash", "input": map[string]any{}},
		},
	}})
	if adapter.VisibleOutput() != "最终结论" {
		t.Fatalf("latest assistant text = %q", adapter.VisibleOutput())
	}
	adapter.handleClaudeStream(map[string]any{"type": "result", "result": "final answer", "is_error": false})
	if adapter.VisibleOutput() != "final answer" {
		t.Fatalf("terminal result should replace assistant text, got %q", adapter.VisibleOutput())
	}
}

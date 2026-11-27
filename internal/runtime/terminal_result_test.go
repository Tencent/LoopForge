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

func TestVisibleOutputPreservesPreGateCitedReportOverShortClosing(t *testing.T) {
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, nil)
	report := "# 知识库验收报告\n\n核心结论来自玄武-2026 [[knowledge:ev_abc123_1]]。"
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": report}},
	}})
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "tool_use", "id": "gate-1", "name": "mcp__agentflow-gate__submit_gate_result", "input": map[string]any{"decision": "pass", "reason": "ok"}},
		},
	}})
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "节点终态已提交为 pass。"}},
	}})
	adapter.handleClaudeStream(map[string]any{"type": "result", "result": "节点终态已提交为 pass。", "is_error": false})
	if adapter.VisibleOutput() != report {
		t.Fatalf("expected pre-gate cited report, got %q", adapter.VisibleOutput())
	}
}

func TestVisibleOutputDoesNotLeakIntermediateChatterOrMaskFailure(t *testing.T) {
	// 1. Intermediate chatter with a citation followed by an uncited pre-gate conclusion must not revert to chatter.
	adapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, nil)
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "我先查到了 [[knowledge:ev_old_1]]，下面改代码。"}},
	}})
	adapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "代码已修改并通过单测。"},
			map[string]any{"type": "tool_use", "id": "gate-1", "name": "submit_gate_result", "input": map[string]any{"decision": "pass", "reason": "ok"}},
		},
	}})
	adapter.handleClaudeStream(map[string]any{"type": "result", "result": "全部完成。", "is_error": false})
	if adapter.VisibleOutput() != "全部完成。" {
		t.Fatalf("unexpected fallback to earlier chatter: %q", adapter.VisibleOutput())
	}

	// 2. Failed result must never be masked by pre-gate cited output.
	failedAdapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, nil)
	failedAdapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "结论 [[knowledge:ev_fail_1]]"},
			map[string]any{"type": "tool_use", "id": "gate-2", "name": "submit_gate_result", "input": map[string]any{}},
		},
	}})
	failedAdapter.handleClaudeStream(map[string]any{"type": "result", "result": "Max turns (120) exceeded", "is_error": true})
	if failedAdapter.VisibleOutput() != "Max turns (120) exceeded" {
		t.Fatalf("failed result was masked: %q", failedAdapter.VisibleOutput())
	}

	// 3. Inline code marker does not count as a real citation.
	codeAdapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, nil)
	codeAdapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "示例：`[[knowledge:ev_code_1]]`"},
			map[string]any{"type": "tool_use", "id": "gate-3", "name": "submit_gate_result", "input": map[string]any{}},
		},
	}})
	codeAdapter.handleClaudeStream(map[string]any{"type": "result", "result": "最终回复", "is_error": false})
	if codeAdapter.VisibleOutput() != "最终回复" {
		t.Fatalf("code-only marker should not override final result: %q", codeAdapter.VisibleOutput())
	}

	// 4. Substantial new post-gate conclusion must not be masked by pre-gate cited output.
	longPostGate := strings.Repeat("这是门禁提交后补充的完整排障分析与修复报告，不包含知识库引用但属于实质性终态输出。", 4)
	postGateAdapter := newProtocolAdapter(domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}, nil)
	postGateAdapter.handleClaudeStream(map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "前序引用 [[knowledge:ev_pre_1]]"},
			map[string]any{"type": "tool_use", "id": "gate-4", "name": "submit_gate_result", "input": map[string]any{}},
		},
	}})
	postGateAdapter.handleClaudeStream(map[string]any{"type": "result", "result": longPostGate, "is_error": false})
	if postGateAdapter.VisibleOutput() != longPostGate {
		t.Fatalf("substantial post-gate conclusion was masked: %q", postGateAdapter.VisibleOutput())
	}
}

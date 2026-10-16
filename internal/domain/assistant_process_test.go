package domain

import (
	"strings"
	"testing"
)

func TestAssistantProcessMergesThinkingAndStaysOutOfPrompts(t *testing.T) {
	var steps []AssistantProcessStep
	steps = AppendAssistantProcess(steps, "thinking", "先读", false)
	steps = AppendAssistantProcess(steps, "thinking", "需求", false)
	steps = AppendAssistantProcess(steps, "thinking", "先读需求", true)
	steps = AppendAssistantProcess(steps, "status", "正在调用模型…", false)
	steps = AppendAssistantProcess(steps, "tool", "issues_list", false)
	steps = AppendAssistantProcess(steps, "thinking", "再回答", false)
	if len(steps) != 3 || steps[0].Text != "先读需求" || steps[1] != (AssistantProcessStep{Kind: "tool", Text: "issues_list"}) || steps[2].Text != "再回答" {
		t.Fatalf("steps: %+v", steps)
	}
	long := []AssistantProcessStep{{Kind: "thinking", Text: strings.Repeat("思", assistantProcessMaxChars)}, {Kind: "tool", Text: "x"}, {Kind: "script", Text: "<b>"}}
	clean := SanitizeAssistantProcess(long)
	if len(clean) != 1 || len(clean[0].Text) > assistantProcessMaxChars || !strings.HasPrefix(clean[0].Text, "思") || !strings.HasSuffix(clean[0].Text, "思") {
		t.Fatalf("sanitize must bound size and keep valid UTF-8: %d steps", len(clean))
	}
	history := []AssistantMessage{{Role: "assistant", Content: "ok", Process: steps}}
	if WithoutAssistantProcess(history)[0].Process != nil || history[0].Process == nil {
		t.Fatal("prompt history must drop process without mutating the stored session")
	}
}

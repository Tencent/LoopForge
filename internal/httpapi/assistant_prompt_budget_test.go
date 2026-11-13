package httpapi

import (
	"strconv"
	"strings"
	"testing"
)

func budgetTestMessages(turns int, payload string) []assistantMessage {
	messages := make([]assistantMessage, 0, turns*2+1)
	for i := 0; i < turns; i++ {
		messages = append(messages,
			assistantMessage{Role: "user", Content: "question " + strconv.Itoa(i) + " " + payload + strings.Repeat("问", 200)},
			assistantMessage{Role: "assistant", Content: "answer " + strconv.Itoa(i) + " " + payload + strings.Repeat("答", 200)},
		)
	}
	return append(messages, assistantMessage{Role: "user", Content: "latest question"})
}

func TestAssistantPromptFitsBudgetForLongHistory(t *testing.T) {
	h := &assistantHandlers{}
	req := assistantRequest{Messages: budgetTestMessages(40, strings.Repeat("历史正文", 2000))}
	prompt := h.turnPrompt(req, "", false)
	if budget := assistantPromptBudget(); len(prompt) > budget {
		t.Fatalf("prompt %d bytes exceeds budget %d", len(prompt), budget)
	}
	if !strings.Contains(prompt, "对话历史已压缩") {
		t.Fatal("compaction digest missing")
	}
	if !strings.Contains(prompt, "latest question") {
		t.Fatal("latest question was dropped")
	}
}

func TestAssistantPromptKeepsRecentTurns(t *testing.T) {
	h := &assistantHandlers{}
	req := assistantRequest{Messages: budgetTestMessages(40, strings.Repeat("历史正文", 2000))}
	prompt := h.turnPrompt(req, "", false)
	for _, want := range []string{"question 39", "answer 39", "latest question"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("recent turn %q missing from prompt", want)
		}
	}
	if strings.Contains(prompt, "question 0 ") {
		t.Fatal("oldest turn should have been compacted away")
	}
	if !strings.Contains(prompt, "answer 39") {
		t.Fatal("recent answer missing from prompt")
	}
}

func TestAssistantPromptBudgetHonoursEnvironment(t *testing.T) {
	t.Setenv(assistantPromptBudgetEnv, "16384")
	if got := assistantPromptBudget(); got != 16384 {
		t.Fatalf("budget = %d", got)
	}
	h := &assistantHandlers{}
	req := assistantRequest{Messages: budgetTestMessages(40, strings.Repeat("历史正文", 2000))}
	if prompt := h.turnPrompt(req, "", false); len(prompt) > 16384 {
		t.Fatalf("prompt %d bytes exceeds configured budget", len(prompt))
	}
}

func TestAssistantPromptBudgetIgnoresInvalidEnvironment(t *testing.T) {
	for _, raw := range []string{"", "abc", "1024", "-1"} {
		t.Setenv(assistantPromptBudgetEnv, raw)
		if got := assistantPromptBudget(); got != assistantPromptBudgetDefault {
			t.Fatalf("budget(%q) = %d, want default", raw, got)
		}
	}
}

func TestAssistantPromptCapsOneOversizedMessage(t *testing.T) {
	h := &assistantHandlers{}
	req := assistantRequest{Messages: []assistantMessage{
		{Role: "user", Content: "earlier"},
		{Role: "assistant", Content: strings.Repeat("长", 100000)},
		{Role: "user", Content: "latest question"},
	}}
	prompt := h.turnPrompt(req, "", false)
	if budget := assistantPromptBudget(); len(prompt) > budget {
		t.Fatalf("prompt %d bytes exceeds budget %d", len(prompt), budget)
	}
	if !strings.Contains(prompt, "已省略") {
		t.Fatal("oversized message was not marked as truncated")
	}
	if !strings.Contains(prompt, "latest question") {
		t.Fatal("latest question was dropped")
	}
}

// A steered follow-up resumes the native session and repeats the interrupted
// reply, which is the one resume path that can carry an unbounded body.
func TestAssistantPromptBudgetCoversResumeSteer(t *testing.T) {
	h := &assistantHandlers{}
	req := assistantRequest{Messages: []assistantMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: strings.Repeat("已完成的分析", 20000)},
		{Role: "user", Content: "改一下方向", Trigger: "steer"},
	}}
	prompt := h.turnPrompt(req, "", true)
	if budget := assistantPromptBudget(); len(prompt) > budget {
		t.Fatalf("resume prompt %d bytes exceeds budget %d", len(prompt), budget)
	}
	if !strings.Contains(prompt, "改一下方向") {
		t.Fatal("steered instruction was dropped")
	}
}

// Branching and rebuilding a lost native session both replay the stored
// history through the same first-turn prompt, so the budget must hold there.
func TestAssistantPromptBudgetCoversBranchAndRebuild(t *testing.T) {
	h := &assistantHandlers{}
	for _, mode := range []string{"branch", "rebuild"} {
		t.Run(mode, func(t *testing.T) {
			// Both paths build the first-turn prompt from the stored messages;
			// branch sessions simply carry the parent id on the session row.
			req := assistantRequest{Messages: budgetTestMessages(40, strings.Repeat("历史正文", 2000))}
			prompt := h.turnPrompt(req, "", false)
			if budget := assistantPromptBudget(); len(prompt) > budget {
				t.Fatalf("%s prompt %d bytes exceeds budget %d", mode, len(prompt), budget)
			}
			if !strings.Contains(prompt, "latest question") {
				t.Fatalf("%s dropped the latest question", mode)
			}
		})
	}
}

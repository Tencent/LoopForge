package httpapi

import (
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// assistantPromptBudgetDefault bounds the history replayed into one chat
	// prompt. A long conversation keeps growing, and branching or rebuilding a
	// lost native session replays all of it, so without a budget the prompt
	// eventually exceeds the model's window and the turn fails.
	assistantPromptBudgetDefault = 96 << 10
	// assistantPromptMessageCap bounds one replayed message so a single large
	// tool output or pasted log cannot crowd out the rest of the conversation.
	assistantPromptMessageCap = 8 << 10
	// assistantPromptBudgetFloor keeps an explicit configuration from making
	// the conversation unusably short.
	assistantPromptBudgetFloor = 8 << 10
	assistantPromptBudgetEnv   = "AGENTFLOW_CHAT_PROMPT_BUDGET_BYTES"
)

// assistantPromptBudget is the byte budget for the prompt turnPrompt builds.
// The executor's system instructions are added by the transport and are not
// part of this budget.
func assistantPromptBudget() int {
	raw := strings.TrimSpace(os.Getenv(assistantPromptBudgetEnv))
	if raw == "" {
		return assistantPromptBudgetDefault
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < assistantPromptBudgetFloor {
		return assistantPromptBudgetDefault
	}
	return value
}

// assistantTruncate shortens content to at most limit bytes on rune boundaries,
// keeping its head and tail and marking the removed middle.
func assistantTruncate(content string, limit int) string {
	if limit <= 0 || len(content) <= limit {
		return content
	}
	// Reserve the marker's own bytes so the result never exceeds limit.
	const markerReserve = 40
	if limit <= markerReserve {
		return content[:limit]
	}
	body := limit - markerReserve
	head := body * 3 / 5
	tail := body - head
	if head > len(content) {
		head = len(content)
	}
	for head > 0 && !utf8.RuneStart(content[head]) {
		head--
	}
	start := len(content) - tail
	if start < head {
		start = head
	}
	for start < len(content) && !utf8.RuneStart(content[start]) {
		start++
	}
	removed := start - head
	if removed <= 0 {
		return content
	}
	return content[:head] + "\n…[已省略 " + strconv.Itoa(removed) + " 字节]…\n" + content[start:]
}

// assistantMessageCost approximates one replayed message's serialized size,
// including the JSON separator and field names.
func assistantMessageCost(m assistantMessage) int {
	return len(m.Role) + len(m.Content) + 48
}

// fitAssistantHistory keeps the newest messages that fit in limit bytes and
// returns a digest describing what it dropped. The last message is the question
// being asked, so it is kept whenever it can fit at all.
func fitAssistantHistory(messages []assistantMessage, limit int) ([]assistantMessage, string) {
	if len(messages) == 0 {
		return messages, ""
	}
	if limit < assistantPromptBudgetFloor {
		limit = assistantPromptBudgetFloor
	}
	prepared := make([]assistantMessage, len(messages))
	copy(prepared, messages)
	last := len(prepared) - 1
	// The final user message is the actual question: trim it only when it alone
	// would consume most of the budget.
	if cap := limit * 3 / 4; len(prepared[last].Content) > cap {
		prepared[last].Content = assistantTruncate(prepared[last].Content, cap)
	}
	for i := 0; i < last; i++ {
		if len(prepared[i].Content) > assistantPromptMessageCap {
			prepared[i].Content = assistantTruncate(prepared[i].Content, assistantPromptMessageCap)
		}
	}
	kept, size := 0, 0
	for i := last; i >= 0; i-- {
		cost := assistantMessageCost(prepared[i])
		if kept > 0 && size+cost > limit {
			break
		}
		size += cost
		kept++
	}
	start := len(prepared) - kept
	if start <= 0 {
		return prepared, ""
	}
	droppedBytes := 0
	for _, m := range prepared[:start] {
		droppedBytes += len(m.Content)
	}
	digest := "对话历史已压缩：为控制提示词长度，省略了更早的 " + strconv.Itoa(start) +
		" 条消息（原文约 " + strconv.Itoa(droppedBytes) + " 字节）。这些消息不在当前上下文中，" +
		"如需其中内容请让用户重新提供或重新查询，不要凭猜测复述。\n"
	return prepared[start:], digest
}

package httpapi

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

// issue #430: conversation prompts are rebuilt from the full message
// history every turn. The OS argv limit (#429) only avoids a process
// error; it does not keep the prompt within the model's context window.
// Long histories, branched/edited histories, and native-session
// recovery all replay the full history, which can exceed the model's
// input budget.
//
// This file adds a model-aware, bounded projection of the conversation
// history. The projection is applied to the model-facing copy only;
// the authoritative user-visible history in the session is never
// mutated. Bytes and tokens are tracked separately: the budget is
// expressed in estimated model tokens, not UTF-8 bytes.

const (
	// DefaultModelInputTokens is the default model input budget when
	// no context window is known for the selected model.
	DefaultModelInputTokens = 128000

	// AssistantReservedTokens reserves space for the system prompt,
	// tool schemas, and expected model output, so the history
	// projection does not consume the entire context window.
	AssistantReservedTokens = 8000

	// assistantPromptTokenBudgetEnv allows operators to override the
	// total token budget without changing code.
	assistantPromptTokenBudgetEnv = "AGENTFLOW_CHAT_PROMPT_TOKEN_BUDGET"
)

// assistantTurnTokenBudget returns the token budget for a conversation
// turn. It reads the environment override when set, otherwise falls
// back to the default model input budget minus the reserved amount for
// system instructions, tool schemas, and expected output.
func assistantTurnTokenBudget(model string) int {
	if raw := strings.TrimSpace(os.Getenv(assistantPromptTokenBudgetEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	_ = model
	if DefaultModelInputTokens <= AssistantReservedTokens {
		return 0
	}
	return DefaultModelInputTokens - AssistantReservedTokens
}

// estimateTokens provides a rough token estimate for a text string.
// It counts Unicode runes and approximates roughly four runes per
// token, which is a common heuristic for modern LLM tokenizers. The
// estimate is deliberately separate from the UTF-8 byte length: a
// CJK character is one rune (≈1 token here) but three UTF-8 bytes.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	n := len([]rune(text))
	if n <= 4 {
		return 1
	}
	return (n + 3) / 4
}

// carriesDrafts reports whether an assistant message content carries
// a drafts list (saved or pending operation drafts). Messages with
// pending drafts must never be dropped from the projection.
func carriesDrafts(content string) bool {
	if !strings.Contains(content, "drafts") {
		return false
	}
	var obj map[string]any
	if json.Unmarshal([]byte(content), &obj) != nil {
		return false
	}
	drafts, ok := obj["drafts"]
	if !ok {
		return false
	}
	arr, ok := drafts.([]any)
	return ok && len(arr) > 0
}

// lastUserIndex returns the index of the last user message in the
// history, or -1 if there is none.
func lastUserIndex(messages []domain.AssistantMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return i
		}
	}
	return -1
}

// assistantHistoryProjection builds a bounded, model-facing projection
// of the conversation history. It walks from the newest message to
// the oldest, accumulating token estimates, and stops when the budget
// is exhausted. The latest user message and assistant messages that
// carry drafts are always preserved regardless of budget. The input
// slice is never mutated; the projection is a separate copy.
//
// The projection is deterministic for a given input and budget.
func assistantHistoryProjection(
	messages []domain.AssistantMessage,
	tokenBudget int,
	estimator func(string) int,
) []domain.AssistantMessage {
	if len(messages) == 0 {
		return nil
	}

	lastUser := lastUserIndex(messages)

	// Collect newest-first, then reverse to chronological order.
	kept := make([]domain.AssistantMessage, 0, len(messages))
	used := 0

	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		cost := estimator(m.Content)

		// The latest user message is always kept.
		if i == lastUser {
			kept = append(kept, m)
			continue
		}

		// Assistant messages carrying drafts are always kept so the
		// model still sees pending operations.
		if m.Role == "assistant" && carriesDrafts(m.Content) {
			kept = append(kept, m)
			continue
		}

		if used+cost <= tokenBudget {
			used += cost
			kept = append(kept, m)
		} else {
			break
		}
	}

	// Reverse to chronological order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return kept
}

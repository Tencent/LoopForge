package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// issue #430: a conversation's prompt is rebuilt every turn from the full
// message history, so long histories (including saved operation drafts)
// can grow past the model's context window. The OS argv limit is gone
// (#429), but that is not a model budget. The history projection sent to
// the model must be bounded by the model's input budget, while the
// authoritative user-visible history is never mutated.
//
// The projection below is applied to the model-facing copy only; the
// stored conversation messages are left untouched.

func assistantBudgetMessages(messages []domain.AssistantMessage, tokenBudget int) []domain.AssistantMessage {
	return assistantHistoryProjection(messages, tokenBudget, estimateTokens)
}

// TestAssistantHistoryProjectionIsBounded verifies that the history
// projection respects the token budget.
func TestAssistantHistoryProjectionIsBounded(t *testing.T) {
	budget := 200
	history := []domain.AssistantMessage{
		{Role: "user", Content: "第一轮问题"},
		{Role: "assistant", Content: "第一轮回答"},
		{Role: "user", Content: "第二轮问题"},
		{Role: "assistant", Content: "第二轮回答"},
		{Role: "user", Content: "第三轮问题"},
	}
	projected := assistantBudgetMessages(history, budget)

	total := 0
	for _, m := range projected {
		total += estimateTokens(m.Content)
	}
	if total > budget {
		t.Fatalf("projection over budget: projected=%d messages=%d", total, len(projected))
	}
}

// TestAssistantHistoryProjectionPreservesNewest asserts the newest
// user message is present in the projection.
func TestAssistantHistoryProjectionPreservesNewest(t *testing.T) {
	budget := 120
	history := []domain.AssistantMessage{
		{Role: "user", Content: "最早的用户消息"},
		{Role: "assistant", Content: "最早的助手回答"},
		{Role: "user", Content: "最新的用户消息"},
	}
	projected := assistantBudgetMessages(history, budget)
	if len(projected) == 0 {
		t.Fatal("projection dropped everything")
	}
	last := projected[len(projected)-1]
	if last.Role != "user" || last.Content != "最新的用户消息" {
		t.Fatalf("newest message dropped: got %+v", last)
	}
}

// TestAssistantHistoryProjectionKeepsRecentWhenOversized: with a
// budget smaller than the accumulated tokens, the projection keeps the
// most recent messages.
func TestAssistantHistoryProjectionKeepsRecentWhenOversized(t *testing.T) {
	budget := 60
	history := []domain.AssistantMessage{
		{Role: "user", Content: "一"},
		{Role: "assistant", Content: "二"},
		{Role: "user", Content: "三"},
		{Role: "assistant", Content: "四"},
		{Role: "user", Content: "五"},
	}
	projected := assistantBudgetMessages(history, budget)
	if len(projected) == 0 {
		t.Fatal("projection dropped everything")
	}
	if projected[len(projected)-1].Content != "五" {
		t.Fatalf("newest message missing: got %+v", projected)
	}
}

// TestAssistantHistoryProjectionNeverDropsLatestUser verifies the
// newest user message is always preserved even under a tight budget.
func TestAssistantHistoryProjectionNeverDropsLatestUser(t *testing.T) {
	history := []domain.AssistantMessage{
		{Role: "user", Content: strings.Repeat("a", 300)},
		{Role: "user", Content: "keep me"},
	}
	projected := assistantBudgetMessages(history, 20)

	found := false
	for _, m := range projected {
		if m.Content == "keep me" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("latest user message dropped: projected=%d", len(projected))
	}
}

// TestAssistantHistoryProjectionPreservesPendingDrafts verifies that
// assistant content containing drafts is retained in the projection.
func TestAssistantHistoryProjectionPreservesPendingDrafts(t *testing.T) {
	draftReplyUnsaved := domain.AssistantMessage{
		Role:    "assistant",
		Content: `{"message":"work in progress","drafts":[{"tool":"x"}]}`,
	}
	history := []domain.AssistantMessage{
		{Role: "user", Content: strings.Repeat("a", 100)},
		draftReplyUnsaved,
	}
	projected := assistantBudgetMessages(history, 40)

	found := false
	for _, m := range projected {
		if strings.Contains(m.Content, "drafts") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("assistant message with drafts dropped; projected=%+v", projected)
	}
}

// TestAssistantHistoryProjectionByteAndTokenSeparated verifies bytes
// and tokens are tracked separately.
func TestAssistantHistoryProjectionByteAndTokenSeparated(t *testing.T) {
	raw := strings.Repeat("字节", 50)
	tokens := estimateTokens(raw)
	bytes := len(raw)

	if tokens == 0 {
		t.Fatal("estimate tokens returned 0 for non-empty text")
	}
	if tokens == bytes {
		t.Fatalf("expected token estimate and byte count to differ: tokens=%d bytes=%d", tokens, bytes)
	}
}

// TestAssistantHistoryProjectionIsDeterministic: same input and budget
// yields the same projection.
func TestAssistantHistoryProjectionIsDeterministic(t *testing.T) {
	history := []domain.AssistantMessage{
		{Role: "user", Content: strings.Repeat("k", 100)},
		{Role: "assistant", Content: strings.Repeat("r", 100)},
		{Role: "user", Content: "end"},
	}
	first := assistantBudgetMessages(history, 100)
	second := assistantBudgetMessages(history, 100)
	if len(first) != len(second) {
		t.Fatalf("non-deterministic: %d vs %d", len(first), len(second))
	}
}

// TestAssistantHistoryProjectionSerialized: the projection survives
// serialization to JSON.
func TestAssistantHistoryProjectionSerialized(t *testing.T) {
	history := []domain.AssistantMessage{
		{Role: "user", Content: strings.Repeat("x", 200)},
		{Role: "assistant", Content: strings.Repeat("y", 200)},
		{Role: "user", Content: "recent"},
	}
	projected := assistantBudgetMessages(history, 40)
	if _, err := json.Marshal(projected); err != nil {
		t.Fatalf("projection not serializable: %v", err)
	}
}

// TestAssistantHistoryProjectionKeepsEverythingUnderBudget: with a
// large budget nothing is dropped.
func TestAssistantHistoryProjectionKeepsEverythingUnderBudget(t *testing.T) {
	history := []domain.AssistantMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	}
	projected := assistantBudgetMessages(history, 10000)
	if len(projected) != len(history) {
		t.Fatalf("under-budget projection dropped messages: got %d want %d", len(projected), len(history))
	}
}

// TestAssistantHistoryProjectionRetainsAllWhenSmall: under a tiny
// budget at least the newest message survives.
func TestAssistantHistoryProjectionRetainsAllWhenSmall(t *testing.T) {
	history := []domain.AssistantMessage{
		{Role: "user", Content: strings.Repeat("u", 100)},
		{Role: "assistant", Content: strings.Repeat("a", 100)},
		{Role: "user", Content: "latest"},
	}
	projected := assistantBudgetMessages(history, 4)
	if len(projected) == 0 {
		t.Fatal("projection dropped everything under tiny budget")
	}
	if projected[len(projected)-1].Content != "latest" {
		t.Fatalf("latest message missing: %+v", projected)
	}
}

// TestAssistantHistoryProjectionLastIsUserRole: the last projected
// message is always the latest user message.
func TestAssistantHistoryProjectionLastIsUserRole(t *testing.T) {
	projected := assistantBudgetMessages([]domain.AssistantMessage{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "last"},
	}, 40)
	if len(projected) == 0 {
		t.Fatal("dropped everything")
	}
	if projected[len(projected)-1].Role != "user" {
		t.Fatalf("last projected message is %q, want user", projected[len(projected)-1].Role)
	}
}

// TestAssistantEstimateTokens verifies the token estimate is
// monotonic and non-zero for non-empty text.
func TestAssistantEstimateTokens(t *testing.T) {
	if got := estimateTokens(""); got != 0 {
		t.Fatalf("empty text estimates %d tokens", got)
	}
	for _, text := range []string{"a", "hello", strings.Repeat("工具输出", 500), " 多字节 文字 "} {
		got := estimateTokens(text)
		if got <= 0 {
			t.Fatalf("non-empty text estimates 0 tokens: %q", text)
		}
	}
}

// TestAssistantEstimateTokensMonotonic: longer text estimates more
// tokens (or equal).
func TestAssistantEstimateTokensMonotonic(t *testing.T) {
	a := estimateTokens("hello")
	b := estimateTokens("hello world")
	c := estimateTokens("hello world again")
	if b < a || c < b {
		t.Fatalf("not monotonic: %d %d %d", a, b, c)
	}
}

// TestAssistantAuthoritativeHistoryNotMutated verifies the projection
// is a copy, not a mutation of the input slice.
func TestAssistantAuthoritativeHistoryNotMutated(t *testing.T) {
	original := []domain.AssistantMessage{
		{Role: "user", Content: strings.Repeat("a", 200)},
		{Role: "assistant", Content: strings.Repeat("b", 200)},
		{Role: "user", Content: "latest"},
	}
	before := len(original)
	projected := assistantBudgetMessages(original, 40)

	if len(original) != before {
		t.Fatalf("authoritative history mutated: before=%d after=%d", before, len(original))
	}
	// The original slice still contains the full content.
	if original[0].Content != strings.Repeat("a", 200) {
		t.Fatal("authoritative history content changed")
	}
	// The projection is a separate slice.
	if &projected[0] == &original[0] {
		t.Fatal("projection is not a copy of the input")
	}
}

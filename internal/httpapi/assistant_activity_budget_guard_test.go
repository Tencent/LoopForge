package httpapi

import (
	"strings"
	"testing"
	"unicode/utf8"

	"agentflow/internal/domain"
)

// The budget helpers reject only what goes over a limit: a write that lands
// exactly on a limit is still kept.
func TestAssistantActivityBudgetHelpersAcceptExactLimits(t *testing.T) {
	// growTextLocked: exactly the per-event limit.
	var grow assistantActivityLog
	grow.events = []domain.AssistantActivityEvent{{Kind: "text", Text: strings.Repeat("a", activityTextLimit-1)}}
	grow.bytes = activityTextLimit - 1
	if !grow.growTextLocked(&grow.events[0], "b") || grow.truncated {
		t.Fatal("growTextLocked rejected text that reaches the per-event limit exactly")
	}
	if len(grow.events[0].Text) != activityTextLimit || grow.bytes != activityTextLimit {
		t.Fatalf("growTextLocked bookkeeping: %d %d", len(grow.events[0].Text), grow.bytes)
	}

	// growTextLocked: exactly the total limit.
	total := assistantActivityLog{events: []domain.AssistantActivityEvent{{Kind: "text"}}, bytes: activityBytesLimit - 1}
	if !total.growTextLocked(&total.events[0], "b") || total.truncated || total.bytes != activityBytesLimit {
		t.Fatalf("growTextLocked rejected text that reaches the total limit exactly: %d %v", total.bytes, total.truncated)
	}

	// replaceTextLocked: exactly the per-event limit, and exactly the total limit.
	var replace assistantActivityLog
	replace.events = []domain.AssistantActivityEvent{{Kind: "text", Text: "old"}}
	replace.bytes = 3
	if !replace.replaceTextLocked(&replace.events[0], strings.Repeat("c", activityTextLimit)) || replace.truncated {
		t.Fatal("replaceTextLocked rejected text of exactly the per-event limit")
	}
	if replace.bytes != activityTextLimit {
		t.Fatalf("replaceTextLocked bytes=%d, want %d", replace.bytes, activityTextLimit)
	}
	full := assistantActivityLog{events: []domain.AssistantActivityEvent{{Kind: "text", Text: strings.Repeat("a", 100)}}, bytes: activityBytesLimit}
	if !full.replaceTextLocked(&full.events[0], strings.Repeat("z", 100)) || full.truncated || full.bytes != activityBytesLimit {
		t.Fatal("replaceTextLocked rejected a same-size replacement at the total limit")
	}

	// reserveEventLocked: one slot and one byte left are still enough.
	room := assistantActivityLog{events: make([]domain.AssistantActivityEvent, activityEventLimit-1), bytes: activityBytesLimit - 1}
	if !room.reserveEventLocked(1) || room.truncated {
		t.Fatal("reserveEventLocked rejected an event that exactly fits")
	}
}

// replaceTextLocked must enforce both limits and leave the event untouched
// when it refuses.
func TestAssistantActivityReplaceTextRejectsOverLimits(t *testing.T) {
	var long assistantActivityLog
	long.events = []domain.AssistantActivityEvent{{Kind: "text", Text: "old"}}
	long.bytes = 3
	if long.replaceTextLocked(&long.events[0], strings.Repeat("c", activityTextLimit+1)) || !long.truncated {
		t.Fatal("replaceTextLocked accepted text over the per-event limit")
	}
	if long.events[0].Text != "old" || long.bytes != 3 {
		t.Fatalf("rejected replacement changed state: %q %d", long.events[0].Text, long.bytes)
	}

	heavy := assistantActivityLog{events: []domain.AssistantActivityEvent{{Kind: "text", Text: strings.Repeat("a", 10)}}, bytes: activityBytesLimit}
	if heavy.replaceTextLocked(&heavy.events[0], strings.Repeat("b", 11)) || !heavy.truncated {
		t.Fatal("replaceTextLocked accepted a replacement over the total limit")
	}
	if heavy.events[0].Text != strings.Repeat("a", 10) || heavy.bytes != activityBytesLimit {
		t.Fatalf("rejected replacement changed state: %q %d", heavy.events[0].Text, heavy.bytes)
	}
}

// The call sites in add / appendTextLocked keep their behaviour after the
// helpers were extracted.
func TestAssistantActivityAddUsesTextBudget(t *testing.T) {
	// Consecutive thinking deltas of the same agent join into one event.
	var thinking assistantActivityLog
	for _, event := range []assistantStreamEvent{{Kind: "thinkingStart"}, {Kind: "thinking", Text: "先看"}, {Kind: "thinking", Text: "文件"}} {
		thinking.add(event)
	}
	if len(thinking.events) != 1 || thinking.events[0].Text != "先看文件" || thinking.bytes != len("先看文件") {
		t.Fatalf("thinking deltas not joined: %+v bytes=%d", thinking.events, thinking.bytes)
	}

	// A whitespace-only token is kept inside the current text block.
	var markdown assistantActivityLog
	for _, text := range []string{"## 标题", "\n", "| a |"} {
		markdown.add(assistantStreamEvent{Kind: "text", Text: text})
	}
	if len(markdown.events) != 1 || markdown.events[0].Text != "## 标题\n| a |" {
		t.Fatalf("whitespace token lost: %+v", markdown.events)
	}

	// A full block that extends the streamed prefix replaces it instead of
	// being appended after it.
	var prefix assistantActivityLog
	prefix.add(assistantStreamEvent{Kind: "text", Text: "Hello"})
	prefix.add(assistantStreamEvent{Kind: "text", Text: "Hello world", Full: true})
	if len(prefix.events) != 1 || prefix.events[0].Text != "Hello world" || prefix.bytes != len("Hello world") {
		t.Fatalf("full block not replaced: %+v bytes=%d", prefix.events, prefix.bytes)
	}

	// The block the CLI resends after a tool is dropped.
	var resent assistantActivityLog
	resent.add(assistantStreamEvent{Kind: "text", Text: "已经展示的段落"})
	resent.add(assistantStreamEvent{Kind: "tool", ID: "t1", Tool: "Read", Detail: "a.go"})
	resent.add(assistantStreamEvent{Kind: "text", Text: "已经展示的段落", Full: true})
	if len(resent.events) != 2 {
		t.Fatalf("resent block duplicated: %+v", resent.events)
	}

	// A new text event counts its size against the total budget.
	tight := assistantActivityLog{bytes: activityBytesLimit - 2}
	tight.add(assistantStreamEvent{Kind: "text", Text: "abc"})
	if len(tight.events) != 0 || !tight.truncated {
		t.Fatalf("text event over the total budget was kept: %+v", tight.events)
	}

	// So does a new tool event.
	tool := assistantActivityLog{bytes: activityBytesLimit - 2}
	tool.add(assistantStreamEvent{Kind: "tool", ID: "t1", Tool: "Bash", Detail: "go test"})
	if len(tool.events) != 0 || !tool.truncated {
		t.Fatalf("tool event over the total budget was kept: %+v", tool.events)
	}

	// A new text event is cut to the per-event limit.
	var big assistantActivityLog
	big.add(assistantStreamEvent{Kind: "text", Text: strings.Repeat("字", activityTextLimit+10)})
	if len(big.events) != 1 || utf8.RuneCountInString(big.events[0].Text) > activityTextLimit+1 || !strings.HasSuffix(big.events[0].Text, "…") {
		t.Fatalf("new text event not cut to the per-event limit: %d runes", utf8.RuneCountInString(big.events[0].Text))
	}
}

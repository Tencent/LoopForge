package httpapi

import (
	"strings"
	"testing"
)

func TestAssistantActivityMergesThinkingOfSameAgent(t *testing.T) {
	var log assistantActivityLog
	log.add(assistantStreamEvent{Kind: "thinkingStart", Parent: "main"})
	log.add(assistantStreamEvent{Kind: "thinking", Parent: "main", Text: "先看"})
	log.add(assistantStreamEvent{Kind: "thinking", Parent: "main", Text: "代码"})
	if len(log.events) != 1 || log.events[0].Text != "先看代码" {
		t.Fatalf("thinking was not merged: %+v", log.events)
	}
	if log.bytes != len("先看代码") || log.truncated {
		t.Fatalf("bytes=%d truncated=%v", log.bytes, log.truncated)
	}
}

func TestAssistantActivityAppendsTextAndWhitespaceTokens(t *testing.T) {
	var log assistantActivityLog
	for _, text := range []string{"##", " ", "标题", "\n", "正文"} {
		log.add(assistantStreamEvent{Kind: "text", Text: text})
	}
	want := "## 标题\n正文"
	if len(log.events) != 1 || log.events[0].Text != want {
		t.Fatalf("text = %+v, want one event %q", log.events, want)
	}
	if log.bytes != len(want) || log.truncated {
		t.Fatalf("bytes=%d truncated=%v", log.bytes, log.truncated)
	}
}

func TestAssistantActivityMarksTruncatedWhenEventTextIsFull(t *testing.T) {
	full := strings.Repeat("a", assistantActivityTextLimit)
	for _, tc := range []struct {
		name  string
		first []assistantStreamEvent
		next  assistantStreamEvent
	}{
		{"thinking", []assistantStreamEvent{{Kind: "thinkingStart"}, {Kind: "thinking", Text: full}}, assistantStreamEvent{Kind: "thinking", Text: "b"}},
		{"text", []assistantStreamEvent{{Kind: "text", Text: full}}, assistantStreamEvent{Kind: "text", Text: "b"}},
		{"whitespace", []assistantStreamEvent{{Kind: "text", Text: full}}, assistantStreamEvent{Kind: "text", Text: " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log assistantActivityLog
			for _, event := range tc.first {
				log.add(event)
			}
			log.add(tc.next)
			if !log.truncated || len(log.events) != 1 || log.events[0].Text != full || log.bytes != len(full) {
				t.Fatalf("truncated=%v events=%d bytes=%d", log.truncated, len(log.events), log.bytes)
			}
		})
	}
}

func TestAssistantActivityAppendToLastRespectsLogByteLimit(t *testing.T) {
	log := assistantActivityLog{bytes: assistantActivityByteLimit - 1}
	log.events = append(log.events, assistantStreamEvent{Kind: "text", Text: "x"})
	log.appendToLastLocked(&log.events[0], "yz")
	if !log.truncated || log.events[0].Text != "x" || log.bytes != assistantActivityByteLimit-1 {
		t.Fatalf("over-limit append changed log: %+v bytes=%d", log.events[0], log.bytes)
	}
	log.truncated = false
	log.appendToLastLocked(&log.events[0], "y")
	if log.truncated || log.events[0].Text != "xy" || log.bytes != assistantActivityByteLimit {
		t.Fatalf("at-limit append failed: %+v bytes=%d truncated=%v", log.events[0], log.bytes, log.truncated)
	}
}

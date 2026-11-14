package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

// fillAssistantActivityEvents gives log exactly assistantActivityEventLimit
// one-byte tool events, so only the event count can reject the next one.
func fillAssistantActivityEvents(t *testing.T, log *assistantActivityLog) {
	t.Helper()
	for i := 0; i < assistantActivityEventLimit; i++ {
		log.add(assistantStreamEvent{Kind: "tool", ID: fmt.Sprintf("tool-%d", i), Text: "t"})
	}
	if len(log.events) != assistantActivityEventLimit || log.truncated {
		t.Fatalf("filling to the event limit: events=%d truncated=%v", len(log.events), log.truncated)
	}
}

func TestAssistantActivityEventLimitStopsNewEvents(t *testing.T) {
	t.Run("tool event", func(t *testing.T) {
		var log assistantActivityLog
		fillAssistantActivityEvents(t, &log)
		bytes := log.bytes
		log.add(assistantStreamEvent{Kind: "status", Text: "s"})
		if !log.truncated || len(log.events) != assistantActivityEventLimit || log.bytes != bytes {
			t.Fatalf("event beyond limit was kept: events=%d bytes=%d truncated=%v", len(log.events), log.bytes, log.truncated)
		}
	})
	t.Run("text event", func(t *testing.T) {
		var log assistantActivityLog
		fillAssistantActivityEvents(t, &log)
		bytes := log.bytes
		log.add(assistantStreamEvent{Kind: "text", Text: "正文"})
		if !log.truncated || len(log.events) != assistantActivityEventLimit || log.bytes != bytes {
			t.Fatalf("text beyond limit was kept: events=%d bytes=%d truncated=%v", len(log.events), log.bytes, log.truncated)
		}
	})
	t.Run("one below the limit still fits", func(t *testing.T) {
		var log assistantActivityLog
		for i := 0; i < assistantActivityEventLimit-1; i++ {
			log.add(assistantStreamEvent{Kind: "tool", ID: fmt.Sprintf("tool-%d", i), Text: "t"})
		}
		log.add(assistantStreamEvent{Kind: "text", Text: "正文"})
		if log.truncated || len(log.events) != assistantActivityEventLimit || log.events[len(log.events)-1].Text != "正文" {
			t.Fatalf("last allowed event was rejected: events=%d truncated=%v", len(log.events), log.truncated)
		}
	})
}

func TestAssistantActivityByteLimitStopsNewEvents(t *testing.T) {
	for _, kind := range []string{"status", "text"} {
		t.Run(kind, func(t *testing.T) {
			log := assistantActivityLog{bytes: assistantActivityByteLimit - 1}
			log.add(assistantStreamEvent{Kind: kind, Text: "ab"})
			if !log.truncated || len(log.events) != 0 || log.bytes != assistantActivityByteLimit-1 {
				t.Fatalf("over-limit %s kept: events=%d bytes=%d truncated=%v", kind, len(log.events), log.bytes, log.truncated)
			}
			log.truncated = false
			log.add(assistantStreamEvent{Kind: kind, Text: "a"})
			if log.truncated || len(log.events) != 1 || log.bytes != assistantActivityByteLimit {
				t.Fatalf("at-limit %s rejected: events=%d bytes=%d truncated=%v", kind, len(log.events), log.bytes, log.truncated)
			}
		})
	}
}

func TestAssistantActivityNewEventTextIsCappedAtTextLimit(t *testing.T) {
	long := strings.Repeat("x", assistantActivityTextLimit+10)
	for _, kind := range []string{"status", "text"} {
		t.Run(kind, func(t *testing.T) {
			var log assistantActivityLog
			log.add(assistantStreamEvent{Kind: kind, Text: long})
			want := long[:assistantActivityTextLimit] + "…"
			if len(log.events) != 1 || log.events[0].Text != want {
				t.Fatalf("%s text not capped at %d runes: events=%d len=%d", kind, assistantActivityTextLimit, len(log.events), len(log.events[0].Text))
			}
		})
	}
}

func TestAssistantActivityFullSnapshotReplacementRespectsLimits(t *testing.T) {
	t.Run("text limit", func(t *testing.T) {
		var log assistantActivityLog
		log.add(assistantStreamEvent{Kind: "text", Text: "开头"})
		bytes := log.bytes
		log.add(assistantStreamEvent{Kind: "text", Full: true, Text: "开头" + strings.Repeat("x", assistantActivityTextLimit)})
		if !log.truncated || len(log.events) != 1 || log.events[0].Text != "开头" || log.bytes != bytes {
			t.Fatalf("over-limit snapshot replaced text: %q bytes=%d truncated=%v", log.events[0].Text, log.bytes, log.truncated)
		}
	})
	t.Run("byte limit", func(t *testing.T) {
		log := assistantActivityLog{}
		log.add(assistantStreamEvent{Kind: "text", Text: "ab"})
		log.bytes = assistantActivityByteLimit - 1
		log.add(assistantStreamEvent{Kind: "text", Full: true, Text: "abcd"})
		if !log.truncated || log.events[0].Text != "ab" || log.bytes != assistantActivityByteLimit-1 {
			t.Fatalf("over-limit snapshot replaced text: %q bytes=%d truncated=%v", log.events[0].Text, log.bytes, log.truncated)
		}
		log.truncated = false
		log.add(assistantStreamEvent{Kind: "text", Full: true, Text: "abc"})
		if log.truncated || log.events[0].Text != "abc" || log.bytes != assistantActivityByteLimit {
			t.Fatalf("at-limit snapshot rejected: %q bytes=%d truncated=%v", log.events[0].Text, log.bytes, log.truncated)
		}
	})
}

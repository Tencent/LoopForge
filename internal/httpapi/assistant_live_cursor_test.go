package httpapi

import (
	"agentflow/internal/domain"
	"encoding/json"
	"testing"
)

func cursorEvents(seqs ...int) []domain.AssistantGenerationEvent {
	out := make([]domain.AssistantGenerationEvent, 0, len(seqs))
	for _, s := range seqs {
		out = append(out, domain.AssistantGenerationEvent{Sequence: s})
	}
	return out
}

func TestAssistantEventsAfterFiltersByCursor(t *testing.T) {
	got := assistantEventsAfter(cursorEvents(3, 4, 5, 6), 4)
	if len(got) != 2 || got[0].Sequence != 5 || got[1].Sequence != 6 {
		t.Fatalf("events after 4 = %+v", got)
	}
	for _, events := range [][]domain.AssistantGenerationEvent{nil, cursorEvents(1, 2)} {
		empty := assistantEventsAfter(events, 2)
		if empty == nil || len(empty) != 0 {
			t.Fatalf("empty batch must be non-nil empty slice, got %#v", empty)
		}
		raw, _ := json.Marshal(empty)
		if string(raw) != "[]" {
			t.Fatalf("empty batch json = %s", raw)
		}
	}
}

func TestLatestAssistantEventSequenceNeverMovesBackwards(t *testing.T) {
	cases := []struct {
		events []domain.AssistantGenerationEvent
		cursor int
		want   int
	}{
		{nil, 7, 7},
		{cursorEvents(1, 2, 3), 7, 7},
		{cursorEvents(5, 8, 9), 7, 9},
		{cursorEvents(10), 0, 10},
	}
	for _, tc := range cases {
		if got := latestAssistantEventSequence(tc.events, tc.cursor); got != tc.want {
			t.Fatalf("latest(%+v, %d) = %d, want %d", tc.events, tc.cursor, got, tc.want)
		}
	}
}

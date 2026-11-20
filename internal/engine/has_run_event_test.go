package engine

import (
	"testing"

	"agentflow/internal/domain"
)

func TestHasRunEvent(t *testing.T) {
	cases := []struct {
		name   string
		events []domain.RunEvent
		typ    string
		want   bool
	}{
		{name: "no events", typ: sponsoredRunEvent, want: false},
		{name: "single match", events: []domain.RunEvent{{Type: sponsoredRunEvent}}, typ: sponsoredRunEvent, want: true},
		{name: "no match", events: []domain.RunEvent{{Type: "node_started"}, {Type: "node_completed"}}, typ: sponsoredRunEvent, want: false},
		{name: "match among many", events: []domain.RunEvent{{Type: "node_started"}, {Type: sponsoredRunEvent}, {Type: "node_completed"}}, typ: sponsoredRunEvent, want: true},
		{name: "case sensitive", events: []domain.RunEvent{{Type: "Node_Started"}}, typ: "node_started", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasRunEvent(domain.Run{Events: tc.events}, tc.typ); got != tc.want {
				t.Fatalf("hasRunEvent(%v, %q) = %v, want %v", tc.events, tc.typ, got, tc.want)
			}
		})
	}
}

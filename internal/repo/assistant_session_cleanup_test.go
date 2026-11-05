package repo

import (
	"testing"

	"agentflow/internal/domain"
)

// Bulk source backfill is a manual SQL migration (migrations/021), not a boot
// task; only the single row a client rewrites is inferred here.
func TestInferredAssistantSessionSource(t *testing.T) {
	cases := []struct {
		name string
		item domain.AssistantSession
		want string
	}{
		{"keeps explicit source", domain.AssistantSession{Source: domain.AssistantSourceSOP}, domain.AssistantSourceSOP},
		{"shared run is sop", domain.AssistantSession{Shared: true, RunID: "run-1"}, domain.AssistantSourceSOP},
		{"title prefix is scheduled", domain.AssistantSession{Title: "定时 · 巡检"}, domain.AssistantSourceScheduled},
		{"renamed scheduled is chat", domain.AssistantSession{Title: "定时 · 巡检", TitleEdited: true}, domain.AssistantSourceChat},
		{"ordinary conversation", domain.AssistantSession{Title: "普通对话"}, domain.AssistantSourceChat},
	}
	for _, c := range cases {
		if got := inferredAssistantSessionSource(c.item); got != c.want {
			t.Fatalf("%s: source = %q, want %q", c.name, got, c.want)
		}
	}
}

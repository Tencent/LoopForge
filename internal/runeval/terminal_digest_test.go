package runeval

import (
	"testing"

	"agentflow/internal/domain"
)

func TestBuildFactsKeepsTheLatestEvaluationEvidence(t *testing.T) {
	var events []domain.TelemetryEvent
	for i := 1; i <= maxDigestEvents+25; i++ {
		events = append(events, domain.TelemetryEvent{Sequence: int64(i), Category: "tool", Name: "Read", Phase: "finish"})
		// Progress events must not consume the bounded evidence window.
		events = append(events, domain.TelemetryEvent{Sequence: int64(i), Category: "tool", Name: "Read", Phase: "progress"})
	}
	events = append(events, domain.TelemetryEvent{Sequence: 999, Category: "error", Name: "agent.failed", Message: "final test verification failed"})
	facts := BuildFacts(domain.Run{Telemetry: events}, domain.RunEvalCatalog{})
	if len(facts.Digest) != maxDigestEvents {
		t.Fatalf("digest length=%d", len(facts.Digest))
	}
	if last := facts.Digest[len(facts.Digest)-1]; last.Sequence != 999 || last.Name != "agent.failed" {
		t.Fatalf("terminal evidence discarded: %+v", last)
	}
	if facts.Digest[0].Sequence != 27 {
		t.Fatalf("unexpected retained window starts at %d", facts.Digest[0].Sequence)
	}
	for i := 1; i < len(facts.Digest); i++ {
		if facts.Digest[i].Sequence <= facts.Digest[i-1].Sequence {
			t.Fatal("digest is not in input order")
		}
	}
	if facts.TelemetryCount != len(events) || len(facts.Errors) != 1 {
		t.Fatalf("aggregate facts lost events: %+v", facts)
	}
}

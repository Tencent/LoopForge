package runeval

import (
	"testing"

	"agentflow/internal/domain"
)

func TestBuildFactsCountsToolInvocationsOnce(t *testing.T) {
	var events []domain.TelemetryEvent
	for _, id := range []string{"one", "two"} {
		for _, phase := range []string{"start", "finish"} {
			events = append(events, domain.TelemetryEvent{NodeID: "node", SessionID: "session", Category: "tool", Phase: phase, ToolCallID: id, Tool: &domain.ToolObservation{Name: "Read", CallID: id}})
		}
	}
	facts := BuildFacts(domain.Run{Telemetry: events}, domain.RunEvalCatalog{})
	if len(facts.ToolRepeats) != 0 {
		t.Fatalf("two successful calls became a tool storm: %+v", facts.ToolRepeats)
	}
	failed := false
	finish := domain.TelemetryEvent{NodeID: "node", SessionID: "session", Category: "tool", Phase: "finish", ToolCallID: "three", Tool: &domain.ToolObservation{Name: "Read", CallID: "three", Success: &failed}, Level: "error"}
	events = append(events, finish, finish)
	facts = BuildFacts(domain.Run{Telemetry: events}, domain.RunEvalCatalog{})
	if len(facts.ToolRepeats) != 1 || facts.ToolRepeats[0].Count != 3 || facts.ToolRepeats[0].Failed != 1 {
		t.Fatalf("calls or repeated failure were double-counted: %+v", facts.ToolRepeats)
	}
}

func TestBuildFactsSeparatesReusedToolCallIDs(t *testing.T) {
	var events []domain.TelemetryEvent
	for _, context := range []domain.TelemetryContext{{SessionID: "s1", Attempt: 1}, {SessionID: "s1", Attempt: 2}, {SessionID: "s2", Attempt: 1}} {
		for _, phase := range []string{"start", "finish"} {
			events = append(events, domain.TelemetryEvent{NodeID: "node", Category: "tool", Phase: phase, Context: context, Tool: &domain.ToolObservation{Name: "Read", CallID: "reused"}})
		}
	}
	facts := BuildFacts(domain.Run{Telemetry: events}, domain.RunEvalCatalog{})
	if len(facts.ToolRepeats) != 1 || facts.ToolRepeats[0].Count != 3 {
		t.Fatalf("sessions/attempts lost their separate calls: %+v", facts.ToolRepeats)
	}
}

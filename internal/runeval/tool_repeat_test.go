package runeval

import (
	"testing"

	"agentflow/internal/domain"
)

func TestBumpToolRepeatPicksToolName(t *testing.T) {
	bag := map[string]*domain.RunEvalToolRepeat{}
	bumpToolRepeat(bag, domain.TelemetryEvent{NodeID: "n1", Name: "event-name", Tool: &domain.ToolObservation{Name: "Bash"}})
	bumpToolRepeat(bag, domain.TelemetryEvent{NodeID: "n1", Name: "Read", Tool: &domain.ToolObservation{}})
	bumpToolRepeat(bag, domain.TelemetryEvent{NodeID: "n1"})

	for _, name := range []string{"Bash", "Read", "tool"} {
		item := bag["n1\x00"+name]
		if item == nil {
			t.Fatalf("missing entry for %q in %v", name, bag)
		}
		if item.NodeID != "n1" || item.Tool != name || item.Count != 1 || item.Failed != 0 {
			t.Fatalf("entry %q = %+v", name, *item)
		}
	}
	if len(bag) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(bag))
	}
}

func TestBumpToolRepeatCountsPhasesAndFailures(t *testing.T) {
	bag := map[string]*domain.RunEvalToolRepeat{}
	tool := &domain.ToolObservation{Name: "Bash"}
	for _, ev := range []domain.TelemetryEvent{
		{NodeID: "n1", Tool: tool},
		{NodeID: "n1", Tool: tool, Phase: "start"},
		{NodeID: "n1", Tool: tool, Phase: "progress"},
		{NodeID: "n1", Tool: tool, Phase: "finish"},
		{NodeID: "n1", Tool: tool, Phase: "error"},
		{NodeID: "n1", Tool: tool, Phase: "finish", Level: "error"},
		{NodeID: "n1", Tool: tool, Phase: "progress", Error: &domain.ErrorObservation{Message: "boom"}},
		{NodeID: "n2", Tool: tool, Phase: "start"},
	} {
		bumpToolRepeat(bag, ev)
	}

	n1 := bag["n1\x00Bash"]
	if n1 == nil || n1.Count != 4 || n1.Failed != 3 {
		t.Fatalf("n1 entry = %+v, want Count 4 Failed 3", n1)
	}
	n2 := bag["n2\x00Bash"]
	if n2 == nil || n2.NodeID != "n2" || n2.Count != 1 || n2.Failed != 0 {
		t.Fatalf("n2 entry = %+v, want separate entry Count 1 Failed 0", n2)
	}
}

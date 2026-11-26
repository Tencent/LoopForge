package runeval

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestSOPTraceAvoidsDoubleCountingAndUngroundedFindings(t *testing.T) {
	ok, failed := true, false
	run := domain.Run{
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "node", Name: "查询", AgentID: "role"}}},
		Nodes:            map[string]*domain.NodeRun{"node": {Attempt: 2}},
	}
	event := func(phase, id string, success *bool) domain.TelemetryEvent {
		return domain.TelemetryEvent{NodeID: "node", Phase: phase, Tool: &domain.ToolObservation{Name: "query", CallID: id, Success: success}}
	}
	candidates := SOPTraceCandidates(run, []domain.TelemetryEvent{
		event("finish", "3", &ok), event("start", "3", nil),
		event("finish", "2", &failed), event("finish", "2", &failed), event("start", "2", nil),
		event("finish", "1", &ok), event("start", "1", nil),
	})
	if len(candidates) != 3 || !strings.Contains(candidates[1].Detail, "调用 3 次，失败 1 次") {
		t.Fatalf("unexpected trace candidates: %+v", candidates)
	}
	result, err := ParseSOPTraceResult(`{"findings":[{"index":1},{"index":1},{"index":99},{}]}`, candidates)
	if err != nil || result.Passed || len(result.Findings) != 1 || result.Findings[0].Dimension != "工具调用失败" {
		t.Fatalf("unexpected confirmed findings: %+v, %v", result, err)
	}
}

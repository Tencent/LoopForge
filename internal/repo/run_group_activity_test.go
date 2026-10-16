package repo

import (
	"agentflow/internal/domain"
	"testing"
	"time"
)

func TestRunGroupActivityStateKeepsEvidenceWithoutHeavyBodies(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Now().UTC()
	run := domain.Run{ID: "activity-only", Status: "waiting", Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "succeeded", Attempt: 1}}, Telemetry: []domain.TelemetryEvent{{ID: "proof", Sequence: 1, Category: "verification", Name: "process.finished", Timestamp: at}, {ID: "model-prose", Sequence: 2, Category: "reasoning", Name: "reasoning.delta", Message: "sensitive model prose", Timestamp: at}, {ID: "tool", Sequence: 3, Category: "tool", Name: "tool.call", Tool: &domain.ToolObservation{Name: "Edit"}, Timestamp: at}}, Events: []domain.RunEvent{{ID: "dispatch", Type: "group.dispatched", Timestamp: at}, {ID: "change", Type: "workspace.changed", Timestamp: at}, {ID: "message", Type: "group.message", Message: "private prose", Timestamp: at}}, Artifacts: []domain.Artifact{{ID: "report", NodeID: "n", Content: "private body", CreatedAt: at}}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	got, ok := st.RunGroupActivityState(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	if len(got.Events) != 2 || len(got.Telemetry) != 2 || len(got.Artifacts) != 1 {
		t.Fatalf("missing projected facts: events=%d telemetry=%d artifacts=%d", len(got.Events), len(got.Telemetry), len(got.Artifacts))
	}
	if len(got.Logs) != 0 || got.Artifacts[0].Content != "" || got.Telemetry[0].ID != "proof" || got.Telemetry[1].ID != "tool" {
		t.Fatalf("heavy bodies loaded: %+v", got)
	}
	if _, ok := st.RunGroupActivityState("missing"); ok {
		t.Fatal("missing run returned")
	}
}

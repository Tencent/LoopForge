package engine

import (
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestObservationContextCarriesTapdAndFrozenMetricStage(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutIssue(domain.Issue{ID: "local-issue", ProjectID: "alpha", Source: "tapd", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1001"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "task-1", IssueID: "local-issue", RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "run-1", WorkflowID: "delivery",
		WorkflowSnapshot: domain.WorkflowDefinition{Version: 2, Nodes: []domain.WorkflowNode{{ID: "build", MetricStage: "development"}}},
		Nodes:            map[string]*domain.NodeRun{"build": {NodeID: "build", Attempt: 1}},
	}
	ctx := New(s, nil).observationContext(run, "build", run.Nodes["build"], domain.TelemetryEvent{})
	if ctx.TAPDID != "1001" || ctx.MetricStage != "development" || ctx.TaskID != "task-1" || ctx.IssueID != "local-issue" {
		t.Fatalf("unexpected metrics context: %+v", ctx)
	}
}

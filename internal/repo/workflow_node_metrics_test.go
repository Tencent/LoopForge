package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAccumulateNodeMetricsAppendsNamedNodes(t *testing.T) {
	start := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	def := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "plan", Name: "规划"}}}
	run := domain.Run{Nodes: map[string]*domain.NodeRun{
		"plan":  {Status: "succeeded", Attempt: 2, ReworkCount: 1, StartedAt: start, FinishedAt: start.Add(3 * time.Second)},
		"ghost": {Status: "waiting_approval"},
	}}
	m := &domain.WorkflowMetrics{}
	durations := map[string]int64{}

	accumulateNodeMetrics(m, run, def, durations)

	if len(m.NodeMetrics) != 2 {
		t.Fatalf("node metrics = %#v", m.NodeMetrics)
	}
	byID := map[string]domain.WorkflowNodeMetric{}
	for _, nm := range m.NodeMetrics {
		byID[nm.NodeID] = nm
	}
	plan := byID["plan"]
	if plan.Name != "规划" || plan.Executions != 2 || plan.Succeeded != 1 || plan.Reworks != 1 {
		t.Fatalf("plan metric = %#v", plan)
	}
	if ghost := byID["ghost"]; ghost.Name != "ghost" || ghost.Waiting != 1 || ghost.Executions != 0 {
		t.Fatalf("ghost metric = %#v", ghost)
	}
	if durations["plan"] != 3000 || durations["ghost"] != 0 {
		t.Fatalf("durations = %#v", durations)
	}
}

func TestAccumulateNodeMetricsUpdatesExistingInPlace(t *testing.T) {
	start := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	m := &domain.WorkflowMetrics{NodeMetrics: []domain.WorkflowNodeMetric{
		{NodeID: "a", Name: "A", Executions: 1, Succeeded: 1},
		{NodeID: "b", Name: "B", Failed: 1},
	}}
	durations := map[string]int64{"b": 500}
	run := domain.Run{Nodes: map[string]*domain.NodeRun{
		"b": {Status: "failed", Attempt: 1, StartedAt: start, FinishedAt: start.Add(time.Second)},
	}}

	accumulateNodeMetrics(m, run, domain.WorkflowDefinition{}, durations)

	if len(m.NodeMetrics) != 2 || m.NodeMetrics[0].NodeID != "a" || m.NodeMetrics[1].NodeID != "b" {
		t.Fatalf("node order changed: %#v", m.NodeMetrics)
	}
	if b := m.NodeMetrics[1]; b.Name != "B" || b.Failed != 2 || b.Executions != 1 {
		t.Fatalf("b metric = %#v", b)
	}
	if m.NodeMetrics[0].Executions != 1 || m.NodeMetrics[0].Succeeded != 1 {
		t.Fatalf("a metric touched: %#v", m.NodeMetrics[0])
	}
	if durations["b"] != 1500 {
		t.Fatalf("durations = %#v", durations)
	}
}

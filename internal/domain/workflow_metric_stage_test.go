package domain

import "testing"

func TestValidateWorkflowMetricStages(t *testing.T) {
	workflow := WorkflowDefinition{Nodes: []WorkflowNode{{ID: "code", MetricStage: "development"}, {ID: "approval", MetricStage: "none"}}}
	if err := ValidateWorkflowMetricStages(workflow); err != nil {
		t.Fatal(err)
	}
	workflow.Nodes[0].MetricStage = "develop"
	if err := ValidateWorkflowMetricStages(workflow); err == nil {
		t.Fatal("expected an invalid stage to be rejected")
	}
}

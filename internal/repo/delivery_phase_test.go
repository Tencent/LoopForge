package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRequirementPhaseDurations(t *testing.T) {
	start := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	finish := func(minutes int) time.Time { return start.Add(time.Duration(minutes) * time.Minute) }
	run := domain.Run{
		WorkflowSnapshot: domain.WorkflowDefinition{
			ID: "requirement-delivery",
			Nodes: []domain.WorkflowNode{
				{ID: "shape", Name: "需求澄清"},
				{ID: "code", Name: "后端研发"},
				{ID: "verify", Name: "测试验证"},
			},
		},
		Nodes: map[string]*domain.NodeRun{
			"shape":  {StartedAt: start, FinishedAt: finish(10)},
			"code":   {StartedAt: start, FinishedAt: finish(35)},
			"verify": {StartedAt: start, FinishedAt: finish(12)},
		},
	}
	clarification, development, testDuration := requirementPhaseDurations(run, domain.WorkflowDefinition{})
	if clarification != int64(10*time.Minute/time.Millisecond) {
		t.Fatalf("clarification=%d", clarification)
	}
	if development != int64(35*time.Minute/time.Millisecond) {
		t.Fatalf("development=%d", development)
	}
	if testDuration != int64(12*time.Minute/time.Millisecond) {
		t.Fatalf("test=%d", testDuration)
	}
}

func TestRequirementPhaseMeasurementsPreferFrozenStage(t *testing.T) {
	start := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	run := domain.Run{
		ID: "run-1",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "delivery", Nodes: []domain.WorkflowNode{
			{ID: "test-named-code", Name: "测试脚本", MetricStage: "development"},
			{ID: "verify", Name: "测试验证", MetricStage: "none"},
			{ID: "opaque", Name: "自定义步骤"},
		}},
		Nodes: map[string]*domain.NodeRun{
			"test-named-code": {StartedAt: start, FinishedAt: start.Add(5 * time.Minute)},
			"verify":          {StartedAt: start, FinishedAt: start.Add(2 * time.Minute)},
			"opaque":          {StartedAt: start, FinishedAt: start.Add(time.Minute)},
		},
	}
	measured := requirementPhaseMeasurements(run, domain.WorkflowDefinition{})
	if measured.development != int64(5*time.Minute/time.Millisecond) || measured.test != 0 {
		t.Fatalf("unexpected durations: %+v", measured)
	}
	if measured.samples.Development != 1 || measured.samples.Testing != 0 || measured.samples.Inferred != 0 || measured.unclassified != 1 {
		t.Fatalf("unexpected coverage: %+v", measured)
	}
	if len(measured.evidence) != 1 || measured.evidence[0].RunID != "run-1" || measured.evidence[0].Source != "explicit" {
		t.Fatalf("unexpected evidence: %+v", measured.evidence)
	}
}

func TestRequirementPhaseMeasurementsMarksLegacyInference(t *testing.T) {
	start := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	run := domain.Run{
		ID:               "run-old",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "old", Nodes: []domain.WorkflowNode{{ID: "test", Name: "测试"}}},
		Nodes:            map[string]*domain.NodeRun{"test": {StartedAt: start, FinishedAt: start.Add(3 * time.Minute)}},
	}
	measured := requirementPhaseMeasurements(run, domain.WorkflowDefinition{})
	if measured.samples.Testing != 1 || measured.samples.Inferred != 1 || measured.evidence[0].Source != "inferred" {
		t.Fatalf("unexpected legacy measurement: %+v", measured)
	}
}

func TestRequirementPhaseMeasurementsSkipsComposedSOPNode(t *testing.T) {
	start := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	run := domain.Run{
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "parent", Nodes: []domain.WorkflowNode{{ID: "child", Name: "研发 SOP", Type: "sop", MetricStage: "development"}}},
		Nodes:            map[string]*domain.NodeRun{"child": {StartedAt: start, FinishedAt: start.Add(time.Hour)}},
	}
	measured := requirementPhaseMeasurements(run, domain.WorkflowDefinition{})
	if measured.development != 0 || measured.samples.Development != 0 || len(measured.evidence) != 0 {
		t.Fatalf("composed SOP container counted twice: %+v", measured)
	}
}

func TestClarificationLeadTimeUsesStartToFirstDevelopment(t *testing.T) {
	tests := []struct {
		name         string
		issueStart   bool
		clarifyNode  bool
		wantSample   int
		wantInferred bool
	}{
		{name: "recorded platform clarification", issueStart: true, wantSample: 1},
		{name: "legacy clarification node", clarifyNode: true, wantSample: 1, wantInferred: true},
		{name: "missing start stays uncollected"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			start := time.Now().UTC().Add(-2 * time.Hour)
			issue := domain.Issue{ID: "lead-time-issue", ProjectID: "platform", Source: "local", Kind: "requirement", Status: "done", Title: "Lead time"}
			if tc.issueStart {
				issue.Clarification.StartedAt = start
			}
			if err := s.PutIssue(issue); err != nil {
				t.Fatal(err)
			}
			workflow := domain.WorkflowDefinition{ID: "lead-time-flow", Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build", MetricStage: "development"}, {ID: "test", Name: "Test", MetricStage: "testing"}}}
			nodes := map[string]*domain.NodeRun{
				"build": {StartedAt: start.Add(time.Hour), FinishedAt: start.Add(90 * time.Minute)},
				"test":  {StartedAt: start.Add(95 * time.Minute), FinishedAt: start.Add(105 * time.Minute)},
			}
			if tc.clarifyNode {
				workflow.Nodes = append(workflow.Nodes, domain.WorkflowNode{ID: "shape", Name: "需求澄清"})
				nodes["shape"] = &domain.NodeRun{StartedAt: start, FinishedAt: start.Add(5 * time.Minute)}
			}
			run := domain.Run{ID: "lead-time-run", WorkflowID: workflow.ID, WorkflowSnapshot: workflow, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "platform", IssueID: issue.ID}, Nodes: nodes, CreatedAt: start, UpdatedAt: start.Add(105 * time.Minute)}
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}
			got := s.Analytics(domain.AnalyticsFilter{ProjectID: "platform", RequirementsOnly: true})
			if len(got.Deliveries) != 1 {
				t.Fatalf("deliveries=%+v", got.Deliveries)
			}
			delivery := got.Deliveries[0]
			if delivery.PhaseSamples.Clarification != tc.wantSample || delivery.DevelopmentDurationMs != int64(30*time.Minute/time.Millisecond) || delivery.TestDurationMs != int64(10*time.Minute/time.Millisecond) {
				t.Fatalf("unexpected phase measurements: %+v", delivery)
			}
			if tc.wantSample == 0 {
				if delivery.ClarificationDurationMs != 0 || !delivery.ClarificationStartedAt.IsZero() {
					t.Fatalf("invented a clarification start: %+v", delivery)
				}
				return
			}
			if delivery.ClarificationDurationMs != int64(time.Hour/time.Millisecond) || !delivery.ClarificationStartedAt.Equal(start) || !delivery.DevelopmentStartedAt.Equal(start.Add(time.Hour)) {
				t.Fatalf("clarification boundary incorrect: %+v", delivery)
			}
			var boundary *domain.AnalyticsPhaseEvidence
			for index := range delivery.PhaseEvidence {
				if delivery.PhaseEvidence[index].Stage == "clarification" {
					boundary = &delivery.PhaseEvidence[index]
				}
			}
			if boundary == nil || (boundary.Source == "inferred") != tc.wantInferred {
				t.Fatalf("clarification evidence=%+v", delivery.PhaseEvidence)
			}
			views := s.IssueAnalyticsViews()
			if tc.issueStart && (len(views) != 1 || !views[0].Clarification.StartedAt.Equal(start)) {
				t.Fatalf("analytics view lost clarification start: %+v", views)
			}
		})
	}
}

func TestGroupedTapdClarificationStartSurvivesDifferentRunIssue(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	start := time.Now().UTC().Add(-2 * time.Hour)
	for _, issue := range []domain.Issue{
		{ID: "platform-bound", ProjectID: "platform", Source: "local", Kind: "requirement", TapdID: "1001", Clarification: domain.ClarificationAssessment{StartedAt: start}},
		{ID: "tapd-run", ProjectID: "platform", Source: "tapd", Kind: "requirement", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1001"}},
	} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	workflow := domain.WorkflowDefinition{ID: "delivery", Nodes: []domain.WorkflowNode{{ID: "develop", MetricStage: "development"}}}
	if err := s.PutRun(domain.Run{ID: "tapd-run-1", WorkflowID: workflow.ID, WorkflowSnapshot: workflow, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "platform", IssueID: "tapd-run"}, Nodes: map[string]*domain.NodeRun{"develop": {StartedAt: start.Add(time.Hour), FinishedAt: start.Add(90 * time.Minute)}}, CreatedAt: start.Add(time.Hour), UpdatedAt: start.Add(90 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "platform", RequirementsOnly: true})
	if len(got.Deliveries) != 1 || got.Deliveries[0].ClarificationDurationMs != int64(time.Hour/time.Millisecond) || got.Deliveries[0].PhaseSamples.Clarification != 1 {
		t.Fatalf("grouped TAPD clarification was lost: %+v", got.Deliveries)
	}
}

func TestAnalyticsDeliveryFoldsChildSOPRunIntoParent(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	start := time.Now().UTC().Add(-time.Hour)
	parent := domain.WorkflowDefinition{ID: "parent-sop", Name: "总控", Nodes: []domain.WorkflowNode{{ID: "child", Name: "研发 SOP", Type: "sop", SOPWorkflowID: "child-sop"}}}
	child := domain.WorkflowDefinition{ID: "child-sop", Name: "研发", Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build", MetricStage: "development"}}}
	if err := s.PutIssue(domain.Issue{ID: "iss", ProjectID: "alpha", Title: "nested"}); err != nil {
		t.Fatal(err)
	}
	runs := []domain.Run{
		{
			ID: "run-parent", WorkflowID: parent.ID, WorkflowSnapshot: parent, Status: "running",
			ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "iss"},
			Nodes:            map[string]*domain.NodeRun{"child": {NodeID: "child", Status: "running", StartedAt: start}},
			Usage:            domain.TokenUsage{TotalTokens: 10}, CreatedAt: start, UpdatedAt: start,
		},
		{
			ID: "run-child", WorkflowID: child.ID, WorkflowSnapshot: child, Status: "failed",
			ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "iss", ParentRunID: "run-parent", ParentNodeID: "child"},
			Nodes:            map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "failed", StartedAt: start.Add(time.Minute), FinishedAt: start.Add(3 * time.Minute)}},
			Usage:            domain.TokenUsage{TotalTokens: 5}, CreatedAt: start.Add(time.Minute), UpdatedAt: start.Add(3 * time.Minute),
		},
	}
	for _, run := range runs {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha"})
	if len(got.Deliveries) != 1 {
		t.Fatalf("deliveries=%+v", got.Deliveries)
	}
	d := got.Deliveries[0]
	if d.RunID != "run-parent" || d.WorkflowID != parent.ID || d.RunCount != 1 || d.Tokens != 15 || d.PhaseSamples.Development != 1 {
		t.Fatalf("a child SOP run must fold into its parent: %+v", d)
	}
}

func TestAnalyticsDeliveryDeduplicatesTapdIDAndKeepsPhaseEvidence(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	start := time.Now().UTC().Add(-time.Hour)
	workflow := domain.WorkflowDefinition{ID: "metrics-workflow", Name: "Delivery", Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build", MetricStage: "development"}}}
	for _, issueID := range []string{"local-1", "local-2"} {
		if err := s.PutIssue(domain.Issue{ID: issueID, ProjectID: "alpha", Source: "tapd", Title: issueID, ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1001"}}); err != nil {
			t.Fatal(err)
		}
	}
	for index, issueID := range []string{"local-1", "local-2"} {
		run := domain.Run{
			ID: "run-" + issueID, WorkflowID: workflow.ID, WorkflowSnapshot: workflow, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: issueID},
			Nodes:            map[string]*domain.NodeRun{"build": {NodeID: "build", StartedAt: start.Add(time.Duration(index) * time.Minute), FinishedAt: start.Add(time.Duration(index+1) * time.Minute)}},
			Usage:            domain.TokenUsage{TotalTokens: 100}, CreatedAt: start.Add(time.Duration(index) * time.Minute), UpdatedAt: start.Add(time.Duration(index+1) * time.Minute),
		}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha", IssueSource: "tapd"})
	if len(got.Deliveries) != 1 {
		t.Fatalf("deliveries=%d, want one TAPD story: %+v", len(got.Deliveries), got.Deliveries)
	}
	delivery := got.Deliveries[0]
	if delivery.ExternalID != "1001" || delivery.RunCount != 2 || delivery.Tokens != 200 || delivery.PhaseSamples.Development != 2 || len(delivery.PhaseEvidence) != 2 {
		t.Fatalf("unexpected TAPD delivery: %+v", delivery)
	}
}

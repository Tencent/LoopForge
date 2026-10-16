package repo

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsScopesRunsAndComputesOperationalMetrics(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC().Add(-time.Minute)
	wf := domain.WorkflowDefinition{ID: "delivery", Name: "Delivery", Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build", AgentID: "developer", ExecutorID: "mock"}}}
	for _, run := range []domain.Run{
		{ID: "good", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "alpha"}, Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "succeeded", ReworkCount: 1, Model: "deepseek", Usage: domain.TokenUsage{TotalTokens: 20, CostUSD: .02}, StartedAt: now, FinishedAt: now.Add(time.Second)}}, Usage: domain.TokenUsage{TotalTokens: 20, CostUSD: .02}, ToolCalls: 3, Artifacts: []domain.Artifact{{ID: "a"}}, TelemetrySequence: 1, Telemetry: []domain.TelemetryEvent{{Sequence: 1, Source: domain.TelemetrySource{Adapter: "codebuddy"}}}, LogSequence: 1, Logs: []domain.LogRecord{{Sequence: 1, Level: "info"}}, CreatedAt: now, UpdatedAt: now.Add(2 * time.Second)},
		{ID: "bad", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed", ExecutionContext: domain.TelemetryContext{ProjectID: "alpha"}, Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "failed", Error: "tests failed\nfull detail"}}, CreatedAt: now, UpdatedAt: now.Add(4 * time.Second)},
		{ID: "other", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "beta"}, Nodes: map[string]*domain.NodeRun{}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
	} {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha"})
	if got.Summary.TotalRuns != 2 || got.Summary.SucceededRuns != 1 || got.Summary.FailedRuns != 1 || got.Summary.CompletionRate != 50 || got.Summary.ReworkedRuns != 1 || got.Summary.TotalReworks != 1 {
		t.Fatalf("unexpected summary: %#v", got.Summary)
	}
	if got.Summary.P95DurationMs < got.Summary.P50DurationMs || got.Coverage.RunsWithTelemetry != 1 || got.Coverage.AdapterEvents["codebuddy"] != 1 || len(got.Failures) != 1 || got.Failures[0].Code != "tests failed" {
		t.Fatalf("unexpected analytics detail: %#v", got)
	}
	alpha := s.WorkflowMetricsFor("alpha")
	var delivery domain.WorkflowMetrics
	for _, item := range alpha {
		if item.WorkflowID == wf.ID {
			delivery = item
		}
	}
	if delivery.TotalRuns != 2 || delivery.SucceededRuns != 1 || delivery.FailedRuns != 1 {
		t.Fatalf("project-scoped workflow metrics = %#v", delivery)
	}
}

func TestAnalyticsBuildsLoopDemoMetricsAndTraceChain(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	if err := s.PutIssue(domain.Issue{
		ID: "tapd-1001", ProjectID: "alpha", Source: "tapd", Title: "Loop demo",
		ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1001", ExternalURL: "https://tapd.example/story/1001"},
	}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "loop-demo", Name: "Loop Demo", Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work"}}}
	success, failure := true, false
	runs := []domain.Run{
		{
			ID: "run-manual", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "tapd-1001", TaskID: "task-1", TraceID: "trace-1"},
			Nodes:            map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "succeeded", StartedAt: now, FinishedAt: now.Add(time.Minute)}},
			Usage:            domain.TokenUsage{TotalTokens: 200, CostUSD: .2, CostStatus: "reported"}, ToolCalls: 2,
			Events: []domain.RunEvent{
				{ID: "pause-1", RunID: "run-manual", Type: "run.paused", Timestamp: now.Add(10 * time.Second)},
				{ID: "resume-1", RunID: "run-manual", Type: "run.resumed", Timestamp: now.Add(20 * time.Second)},
				{ID: "decision-1", RunID: "run-manual", NodeID: "work", Type: "gate.approved", Timestamp: now.Add(30 * time.Second)},
			},
			TelemetrySequence: 2,
			Telemetry: []domain.TelemetryEvent{
				{ID: "tool-1", RunID: "run-manual", NodeID: "work", Sequence: 1, Category: "tool", Name: "tool.call", Phase: "finish", Tool: &domain.ToolObservation{Success: &success}},
				{ID: "tool-2", RunID: "run-manual", NodeID: "work", Sequence: 2, Category: "tool", Name: "tool.call", Phase: "finish", Tool: &domain.ToolObservation{Success: &failure}},
			},
			CreatedAt: now, UpdatedAt: now.Add(time.Minute),
		},
		{
			ID: "run-auto", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "tapd-1001", TaskID: "task-2", TraceID: "trace-2"},
			Nodes:            map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "succeeded", StartedAt: now.Add(time.Hour), FinishedAt: now.Add(time.Hour + time.Minute)}},
			Usage:            domain.TokenUsage{TotalTokens: 100}, CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour + time.Minute),
		},
	}
	for _, run := range runs {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha", Scope: "overview"})
	if got.Demo.AverageTokensPerRun != 150 || got.Demo.ToolCallsObserved != 2 || got.Demo.ToolCallsSucceeded != 1 || got.Demo.ToolCallsFailed != 1 || got.Demo.ToolSuccessRate != 50 {
		t.Fatalf("unexpected demo usage metrics: %#v", got.Demo)
	}
	if got.Demo.HumanInterventions != 1 || got.Demo.AverageHumanInterventions != .5 || got.Demo.AutomatedSucceededRuns != 1 || got.Demo.AutomationRate != 50 {
		t.Fatalf("unexpected demo automation metrics: %#v", got.Demo)
	}
	if len(got.RunTraces) != 2 {
		t.Fatalf("run traces=%d want 2: %#v", len(got.RunTraces), got.RunTraces)
	}
	manual := got.RunTraces[1]
	if manual.ExternalID != "1001" || manual.IssueSource != "tapd" || manual.WorkflowTaskID != "task-1" || manual.RunID != "run-manual" || manual.TraceID != "trace-1" || manual.HumanInterventions != 1 || manual.ToolCallsFailed != 1 {
		t.Fatalf("manual trace chain: %#v", manual)
	}
}

func TestRequirementAnalyticsIncludesUnboundPlatformAndManualTapdLink(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	start := time.Now().UTC().Add(-time.Hour)
	workflow := domain.WorkflowDefinition{ID: "requirement-analytics", Name: "Requirement", Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build", MetricStage: "development"}}}
	issues := []domain.Issue{
		{ID: "platform-unbound", ProjectID: "platform", Source: "local", Kind: "requirement", Title: "Unbound"},
		{ID: "platform-bound", ProjectID: "platform", Source: "local", Kind: "requirement", Title: "Bound", TapdID: "1001"},
		{ID: "tapd-1001", ProjectID: "platform", Source: "tapd", Kind: "requirement", Title: "Synced", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1001"}},
		{ID: "gongfeng-issue", ProjectID: "platform", Source: "gongfeng", Kind: "issue", Title: "Bug"},
	}
	for _, issue := range issues {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	for index, issue := range issues {
		run := domain.Run{
			ID: "run-" + issue.ID, WorkflowID: workflow.ID, WorkflowSnapshot: workflow, Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: "platform", IssueID: issue.ID},
			Nodes:            map[string]*domain.NodeRun{"build": {NodeID: "build", StartedAt: start.Add(time.Duration(index) * time.Minute), FinishedAt: start.Add(time.Duration(index+1) * time.Minute)}},
			Usage:            domain.TokenUsage{TotalTokens: 100}, CreatedAt: start.Add(time.Duration(index) * time.Minute), UpdatedAt: start.Add(time.Duration(index+1) * time.Minute),
		}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "platform", RequirementsOnly: true})
	if got.Summary.TotalRuns != 3 || len(got.Deliveries) != 2 {
		t.Fatalf("requirement filter lost platform demand or included bug: runs=%d deliveries=%+v", got.Summary.TotalRuns, got.Deliveries)
	}
	byID := map[string]domain.AnalyticsDelivery{}
	for _, delivery := range got.Deliveries {
		byID[delivery.ExternalID] = delivery
	}
	if byID[""].IssueID != "platform-unbound" || byID[""].RunCount != 1 || byID["1001"].RunCount != 2 || byID["1001"].Tokens != 200 {
		t.Fatalf("unexpected platform/TAPD grouping: %+v", got.Deliveries)
	}
	views := s.IssueAnalyticsViews()
	for _, view := range views {
		if view.ID == "platform-bound" && (view.TapdID != "1001" || view.Kind != "requirement") {
			t.Fatalf("lightweight analytics view lost manual binding: %+v", view)
		}
	}
}

func TestWorkflowMetricsFallsBackToIssueProject(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC().Add(-time.Minute)
	if err := s.PutIssue(domain.Issue{ID: "iss-legacy", ProjectID: "alpha", Title: "legacy"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "legacy-flow", Name: "Legacy"}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{
		ID: "legacy-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{IssueID: "iss-legacy"},
		CreatedAt:        now, UpdatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	alpha := s.WorkflowMetricsFor("alpha")
	var got domain.WorkflowMetrics
	for _, item := range alpha {
		if item.WorkflowID == wf.ID {
			got = item
		}
	}
	if got.TotalRuns != 1 || got.SucceededRuns != 1 {
		t.Fatalf("issue-scoped workflow metrics = %#v", got)
	}
	beta := s.WorkflowMetricsFor("beta")
	for _, item := range beta {
		if item.WorkflowID == wf.ID && item.TotalRuns != 0 {
			t.Fatalf("legacy run leaked into beta metrics: %#v", item)
		}
	}
	alphaSnap := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha"})
	if alphaSnap.Summary.TotalRuns != 1 || alphaSnap.Summary.SucceededRuns != 1 {
		t.Fatalf("analytics should use issue project when run context is empty: %#v", alphaSnap.Summary)
	}
	other := s.Analytics(domain.AnalyticsFilter{ProjectID: "beta"})
	if other.Summary.TotalRuns != 0 {
		t.Fatalf("legacy run leaked into beta analytics: %#v", other.Summary)
	}
}

func TestAnalyticsOverviewScopeSkipsDeliveries(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC().Add(-time.Minute)
	if err := s.PutIssue(domain.Issue{ID: "gf-9", Title: "COUNT 慢 SQL", Source: "gongfeng", ProjectID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "issue-fix-production", Name: "Issue 修复"}
	run := domain.Run{
		ID: "run-ok", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "gf-9"},
		Artifacts:        []domain.Artifact{{NodeID: "mr", Name: "mr-body", Content: "https://git.example.com/org/repo/-/merge_requests/215\n", Attempt: 1, CreatedAt: now}},
		CreatedAt:        now, UpdatedAt: now.Add(2 * time.Minute),
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha", Scope: "overview"})
	if got.Summary.TotalRuns != 1 || got.Summary.SucceededRuns != 1 {
		t.Fatalf("overview summary: %#v", got.Summary)
	}
	if len(got.Deliveries) != 0 || got.Delivery.Issues != 0 {
		t.Fatalf("overview should skip deliveries: delivery=%#v deliveries=%d", got.Delivery, len(got.Deliveries))
	}
}

func TestAnalyticsWIPHonorsFilters(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, issue := range []domain.Issue{
		{ID: "a-match", ProjectID: "alpha", WorkflowID: "wf", Source: "gongfeng", AssigneeUserID: 7, Status: "in_progress"},
		{ID: "a-other-user", ProjectID: "alpha", WorkflowID: "wf", Source: "gongfeng", AssigneeUserID: 8, Status: "in_progress"},
		{ID: "a-other-source", ProjectID: "alpha", WorkflowID: "wf", Source: "tapd", AssigneeUserID: 7, Status: "in_progress"},
		{ID: "b", ProjectID: "beta", WorkflowID: "wf", Source: "gongfeng", AssigneeUserID: 7, Status: "in_progress"},
		{ID: "done", ProjectID: "alpha", WorkflowID: "wf", Source: "gongfeng", AssigneeUserID: 7, Status: "done"},
	} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	filter := domain.AnalyticsFilter{ProjectID: "alpha", WorkflowID: "wf", UserID: "7", IssueSource: "gongfeng", Scope: "overview"}
	if got := s.Analytics(filter).Summary.InProgressIssues; got != 1 {
		t.Fatalf("filtered WIP=%d want 1", got)
	}
}

func TestAnalyticsDeliveriesParseMRFromArtifacts(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC().Add(-time.Minute)
	if err := s.PutIssue(domain.Issue{ID: "gf-9", Title: "COUNT 慢 SQL", Source: "gongfeng", ProjectID: "alpha", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalIID: 9, ExternalURL: "https://git.example.com/org/repo/issues/9"}}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "issue-fix-production", Name: "Issue 修复"}
	run := domain.Run{
		ID: "run-ok", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "gf-9"},
		Usage:            domain.TokenUsage{TotalTokens: 1200, CostUSD: 0.4},
		Artifacts:        []domain.Artifact{{NodeID: "mr", Name: "mr-body", Content: "https://git.example.com/org/repo/-/merge_requests/215\n", Attempt: 1, CreatedAt: now}},
		CreatedAt:        now, UpdatedAt: now.Add(2 * time.Minute),
		Nodes: map[string]*domain.NodeRun{"mr": {NodeID: "mr", Status: "succeeded", ReworkCount: 1, StartedAt: now, FinishedAt: now.Add(2 * time.Minute)}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha"})
	if got.Delivery.Issues != 1 || got.Delivery.SOPSucceeded != 1 || got.Delivery.WithMR != 1 {
		t.Fatalf("delivery summary: %#v", got.Delivery)
	}
	if len(got.Deliveries) != 1 {
		t.Fatalf("deliveries=%d", len(got.Deliveries))
	}
	item := got.Deliveries[0]
	if item.IssueID != "gf-9" || item.Title != "COUNT 慢 SQL" || item.MRIID != 215 || item.MRProject != "org/repo" || item.MRState != "unknown" || item.Delivered {
		t.Fatalf("delivery item: %#v", item)
	}
	if item.Tokens != 1200 || item.Reworks != 1 || !item.SOPSucceeded {
		t.Fatalf("tokens/sop: %#v", item)
	}
}

func TestAnalyticsDeliveriesIgnoresOtherIssueMRInPatch(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC().Add(-time.Minute)
	if err := s.PutIssue(domain.Issue{
		ID: "gf-skillhub-31", Title: "sandbox deadlock", Source: "gongfeng", ProjectID: "alpha",
		DeliveryMRURL:   "https://git.example.com/example/team-project/-/merge_requests/286",
		DeliveryMRState: "integrated",
	}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "issue-fix-production", Name: "Issue 修复"}
	run := domain.Run{
		ID: "run-31", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "cancelled",
		ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "gf-skillhub-31"},
		Artifacts: []domain.Artifact{
			{NodeID: "mr", Name: "repository-patch", Content: "-https://git.example.com/example/team-project/merge_requests/282\n+https://git.example.com/example/team-project/-/merge_requests/286\n"},
			{NodeID: "mr", Name: "mr-body", Content: "https://git.example.com/example/team-project/-/merge_requests/286\n"},
		},
		CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha"})
	if len(got.Deliveries) != 1 {
		t.Fatalf("deliveries=%d", len(got.Deliveries))
	}
	item := got.Deliveries[0]
	if item.MRIID != 286 {
		t.Fatalf("expected !286, got %#v", item)
	}
}

func TestAnalyticsFiltersRunsByIssueAssignee(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC().Add(-time.Minute)
	for _, issue := range []domain.Issue{
		{ID: "iss-mine", ProjectID: "alpha", Title: "mine", AssigneeUserID: 7, Source: "gongfeng"},
		{ID: "iss-other", ProjectID: "alpha", Title: "other", AssigneeUserID: 8, Source: "gongfeng"},
	} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	wf := domain.WorkflowDefinition{ID: "wf", Name: "WF"}
	for _, run := range []domain.Run{
		{ID: "run-assignee-only", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "iss-mine"}, Usage: domain.TokenUsage{TotalTokens: 10, CostUSD: 0.1}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		{ID: "run-responsible", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "iss-other", ResponsibleUserID: 7}, Usage: domain.TokenUsage{TotalTokens: 5, CostUSD: 0.05}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		{ID: "run-other", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed", ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "iss-other", ResponsibleUserID: 8}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
	} {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha", UserID: "7", Scope: "overview"})
	if got.Summary.TotalRuns != 2 {
		t.Fatalf("filtered runs=%d want 2: %#v", got.Summary.TotalRuns, got.Summary)
	}
	if got.Summary.TotalTokens != 15 {
		t.Fatalf("filtered tokens=%d want 15", got.Summary.TotalTokens)
	}
	if len(got.Trend) == 0 {
		t.Fatal("overview should still emit daily trend")
	}
}

func TestAnalyticsTrendCountsUniqueIssuesAndProjectUsage(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	day := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	for _, issue := range []domain.Issue{
		{ID: "iss-a", ProjectID: "alpha", Title: "a", AssigneeUserID: 7},
		{ID: "iss-b", ProjectID: "beta", Title: "b", AssigneeUserID: 7},
	} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	wf := domain.WorkflowDefinition{ID: "wf", Name: "WF"}
	// FinishedAt is the trend bucket for a completed run. PutRun stamps
	// UpdatedAt with the wall clock, so leaving it empty made the month
	// follow today's date instead of the September fixture.
	ended := func(status string, at time.Time) map[string]*domain.NodeRun {
		return map[string]*domain.NodeRun{"n": {NodeID: "n", Status: status, FinishedAt: at}}
	}
	for _, run := range []domain.Run{
		{ID: "a1", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "iss-a", ResponsibleUserID: 7}, Nodes: ended("succeeded", day.Add(time.Minute)), Usage: domain.TokenUsage{TotalTokens: 100, CostUSD: 0.2}, CreatedAt: day, UpdatedAt: day.Add(time.Minute)},
		{ID: "a2", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "failed", ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", IssueID: "iss-a", ResponsibleUserID: 7}, Nodes: ended("failed", day.Add(2*time.Hour)), Usage: domain.TokenUsage{TotalTokens: 40, CostUSD: 0.1}, CreatedAt: day.Add(time.Hour), UpdatedAt: day.Add(2 * time.Hour)},
		{ID: "b1", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "beta", IssueID: "iss-b", ResponsibleUserID: 7}, Nodes: ended("succeeded", day.Add(time.Minute)), Usage: domain.TokenUsage{TotalTokens: 80, CostUSD: 0.15}, CreatedAt: day, UpdatedAt: day.Add(time.Minute)},
	} {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Analytics(domain.AnalyticsFilter{UserID: "7", Scope: "overview"})
	if got.Summary.TotalRuns != 3 || got.Summary.TotalTokens != 220 {
		t.Fatalf("cross-project personal summary = %#v", got.Summary)
	}
	if len(got.Trend) != 1 || got.Trend[0].Runs != 3 || got.Trend[0].Issues != 2 {
		t.Fatalf("daily trend should count unique issues: %#v", got.Trend)
	}
	if len(got.ProjectUsage) != 2 {
		t.Fatalf("project usage=%d want 2: %#v", len(got.ProjectUsage), got.ProjectUsage)
	}
	byProject := map[string]domain.AnalyticsProjectUsage{}
	for _, row := range got.ProjectUsage {
		byProject[row.ProjectID] = row
	}
	if byProject["alpha"].Runs != 2 || byProject["alpha"].Tokens != 140 || byProject["alpha"].UniqueIssues != 1 {
		t.Fatalf("alpha project usage = %#v", byProject["alpha"])
	}
	if byProject["beta"].Runs != 1 || byProject["beta"].Tokens != 80 || byProject["beta"].UniqueIssues != 1 {
		t.Fatalf("beta project usage = %#v", byProject["beta"])
	}
	week := s.Analytics(domain.AnalyticsFilter{UserID: "7", Scope: "overview", Grain: "week"})
	if len(week.Trend) != 1 || week.Trend[0].Issues != 2 || week.Trend[0].Runs != 3 {
		t.Fatalf("weekly trend should keep unique issues: %#v", week.Trend)
	}
	if !strings.Contains(week.Trend[0].Date, "W") {
		t.Fatalf("weekly trend key = %q", week.Trend[0].Date)
	}
	all := s.Analytics(domain.AnalyticsFilter{UserID: "7", Scope: "overview", Grain: "all"})
	if len(all.Trend) != 1 || all.Trend[0].Date != "2026-09" || all.Trend[0].Runs != 3 {
		t.Fatalf("all-time grain should bucket by month: %#v", all.Trend)
	}
}

func TestAnalyticsOverviewIncludesModelUsage(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC().Add(-time.Minute)
	wf := domain.WorkflowDefinition{ID: "wf", Name: "WF", Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build"}}}
	run := domain.Run{
		ID: "run-model", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "alpha"},
		Nodes: map[string]*domain.NodeRun{
			"build": {NodeID: "build", Status: "succeeded", Model: "deepseek-v3", Usage: domain.TokenUsage{TotalTokens: 40, CostUSD: 0.04}, StartedAt: now, FinishedAt: now.Add(time.Second)},
		},
		Usage:     domain.TokenUsage{TotalTokens: 40, CostUSD: 0.04},
		CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha", Scope: "overview"})
	if len(got.Deliveries) != 0 {
		t.Fatalf("overview should still skip deliveries, got %d", len(got.Deliveries))
	}
	if len(got.Models) != 1 || got.Models[0].Key != "deepseek-v3" || got.Models[0].Tokens != 40 {
		t.Fatalf("overview models = %#v", got.Models)
	}
}

package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsCodingOutcomeCohorts(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	at := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "b", "c", "baseline", "reopened", "multi", "sop-only", "unrelated"} {
		owner := int64(7)
		if id == "c" {
			owner = 8
		}
		issue := domain.Issue{ID: id, ProjectID: "coding", Kind: "requirement", AssigneeUserID: owner}
		if id == "reopened" {
			issue.DeliveryState = "reopened"
		}
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
		if id == "unrelated" {
			continue
		}
		run := domain.Run{ID: id, Kind: domain.RunKindWorkspace, Status: "running", CreatedAt: at, UpdatedAt: at, ExecutionContext: domain.TelemetryContext{ProjectID: "coding", IssueID: id, ResponsibleUserID: owner}}
		if id == "sop-only" {
			run.Kind = ""
		}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	// Reopening multiple workspaces does not create more coding requirements.
	for i, when := range []time.Time{at, at.AddDate(0, 0, -1)} {
		if err := s.PutRun(domain.Run{ID: []string{"a-repeat", "a-old"}[i], Kind: domain.RunKindWorkspace, CreatedAt: when, UpdatedAt: when, ExecutionContext: domain.TelemetryContext{ProjectID: "coding", IssueID: "a", ResponsibleUserID: 7}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, spec := range []struct{ issue, state, branch string }{
		{"a", "merged", "main"}, {"a", "merged", "main"}, {"c", "opened", "main"},
		{"baseline", "merged", "agentflow/integration"}, {"reopened", "merged", "main"},
		{"multi", "merged", "main"}, {"multi", "opened", "main"}, {"sop-only", "merged", "main"},
	} {
		if err := s.DB.Create(&mrDeliveryStateRow{MRProject: "coding/repo", MRIID: i + 1, IssueID: spec.issue, State: spec.state, TargetBranch: spec.branch, MergedAt: timePtr(at.Add(time.Hour))}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		user string
		want domain.AnalyticsCodingOutcome
	}{{"", domain.AnalyticsCodingOutcome{Issues: 6, WithMR: 5, Delivered: 1}}, {"7", domain.AnalyticsCodingOutcome{Issues: 5, WithMR: 4, Delivered: 1}}, {"8", domain.AnalyticsCodingOutcome{Issues: 1, WithMR: 1}}} {
		out := s.Analytics(domain.AnalyticsFilter{ProjectID: "coding", UserID: tc.user, Scope: "overview", IncludeDashboard: true, Grain: "month"})
		if len(out.DataErrors) != 0 || out.Dashboard.Coding != tc.want {
			t.Fatalf("scope %q: %+v / %v", tc.user, out.Dashboard.Coding, out.DataErrors)
		}
		for _, p := range out.Dashboard.Trend {
			if p.Date == "2026-11" && p.Coding != tc.want {
				t.Fatalf("period cohort: %+v", p.Coding)
			}
			if p.Date == "2026-10" && tc.user != "8" && p.Coding != (domain.AnalyticsCodingOutcome{Issues: 1, WithMR: 1, Delivered: 1}) {
				t.Fatalf("old cohort should retain its current outcome: %+v", p.Coding)
			}
		}
	}
}

func TestAnalyticsSOPPerformanceUsesRootCohorts(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	at := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	for _, spec := range []struct {
		id, parent, kind string
		owner, elapsed   int64
		when             time.Time
		group            bool
		tools            []bool
	}{
		{"root-a", "", "", 7, 60000, at.AddDate(0, 0, -1), false, []bool{true}},
		{"child", "root-a", "", 8, 300000, at, false, []bool{true, false}},
		{"root-b", "", "", 7, 180000, at, false, []bool{false}},
		{"unknown", "", "", 7, 0, at, false, nil},
		{"root-c", "", "", 8, 300000, at, false, []bool{true}},
		{"group", "", "", 7, 900000, at, true, []bool{true}},
		{"workspace", "", domain.RunKindWorkspace, 7, 900000, at, false, []bool{true}},
	} {
		run := domain.Run{ID: spec.id, Kind: spec.kind, Status: "succeeded", CreatedAt: spec.when, UpdatedAt: spec.when.Add(time.Minute), ExecutionContext: domain.TelemetryContext{ProjectID: "performance", ResponsibleUserID: spec.owner, ParentRunID: spec.parent}}
		if spec.group {
			run.WorkflowSnapshot.CollaborationMode = "group"
		}
		if spec.elapsed > 0 {
			run.Nodes = map[string]*domain.NodeRun{"work": {StartedAt: spec.when, FinishedAt: spec.when.Add(time.Duration(spec.elapsed) * time.Millisecond)}}
		}
		if spec.id == "child" {
			run.Usage.TotalTokens = 900
			run.Nodes["work"].Usage.TotalTokens = 900
		}
		for i, success := range spec.tools {
			ok := success
			run.Telemetry = append(run.Telemetry, domain.TelemetryEvent{ID: spec.id + string(rune('a'+i)), RunID: spec.id, Sequence: int64(i + 1), Category: "tool", Name: "tool.call", Phase: "finish", Tool: &domain.ToolObservation{Success: &ok}})
		}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	out := s.Analytics(domain.AnalyticsFilter{ProjectID: "performance", UserID: "7", Scope: "overview", IncludeDashboard: true, Grain: "month"})
	want := domain.AnalyticsSOPPerformance{MeasuredRuns: 2, AverageExecutionDurationMs: 120000, P95ExecutionDurationMs: 180000, ToolCallsSucceeded: 2, ToolCallsFailed: 2}
	if len(out.DataErrors) != 0 || out.Dashboard.Performance != want || out.Dashboard.SOPRuns != 3 {
		t.Fatalf("root cohort: %+v / %v", out.Dashboard, out.DataErrors)
	}
	if out.Summary.TotalTokens != 0 {
		t.Fatalf("including child outcomes must not widen the personal token/cost scope: %+v", out.Summary)
	}
	for _, p := range out.Dashboard.Trend {
		if p.Date == "2026-10" && (p.Performance.MeasuredRuns != 1 || p.Performance.AverageExecutionDurationMs != 60000 || p.Performance.ToolCallsSucceeded != 2 || p.Performance.ToolCallsFailed != 1) {
			t.Fatalf("child outcomes must belong to their root creation month: %+v", p)
		}
		if p.Date == "2026-11" && (p.SOPRuns != 2 || p.Performance.MeasuredRuns != 1 || p.Performance.AverageExecutionDurationMs != 180000 || p.Performance.ToolCallsSucceeded != 0 || p.Performance.ToolCallsFailed != 1) {
			t.Fatalf("missing durations must not lower the average or inflate samples: %+v", p)
		}
	}
	window := s.Analytics(domain.AnalyticsFilter{ProjectID: "performance", UserID: "7", Scope: "overview", IncludeDashboard: true, From: at.AddDate(0, 0, -1), To: at.Add(-time.Hour)})
	if len(window.DataErrors) != 0 || window.Dashboard.SOPRuns != 1 || window.Dashboard.Performance.ToolCallsSucceeded != 2 || window.Dashboard.Performance.ToolCallsFailed != 1 {
		t.Fatalf("child completion facts outside the creation window must still belong to the selected root: %+v / %v", window.Dashboard, window.DataErrors)
	}
}

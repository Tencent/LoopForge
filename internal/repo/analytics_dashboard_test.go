package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsDashboardCohorts(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	at := time.Date(2026, 11, 22, 16, 30, 0, 0, time.UTC)
	old := at.AddDate(0, 0, -5)
	for _, id := range []string{"sop", "vibe", "mixed", "repeat", "baseline", "unattributed", "other"} {
		project := "dashboard"
		if id == "other" {
			project = "private"
		}
		if err := s.PutIssue(domain.Issue{ID: id, ProjectID: project, Kind: "requirement", Source: "local"}); err != nil {
			t.Fatal(err)
		}
	}
	workflow := domain.WorkflowDefinition{ID: "dashboard-flow", Name: "交付", Nodes: []domain.WorkflowNode{{ID: "review", Name: "代码评审"}}}
	if err := s.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	putRun := func(id, issue, kind, parent, status string, when time.Time, group bool, events []domain.RunEvent) {
		t.Helper()
		snapshot := workflow
		if group {
			snapshot.CollaborationMode = "group"
		}
		project := "dashboard"
		if issue == "other" {
			project = "private"
		}
		run := domain.Run{ID: id, Kind: kind, WorkflowID: workflow.ID, WorkflowSnapshot: snapshot, Status: status, CreatedAt: when, UpdatedAt: when.Add(3 * time.Minute), ExecutionContext: domain.TelemetryContext{ProjectID: project, IssueID: issue, ParentRunID: parent}, Nodes: map[string]*domain.NodeRun{"review": {NodeID: "review", Status: status, StartedAt: when, FinishedAt: when.Add(3 * time.Minute)}}, Events: events}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	decisions := []domain.RunEvent{}
	for _, kind := range []string{"gate.approved", "node.passed", "gate.rejected", "node.retry_requested", "agent.turns.declined", "run.paused", "run.resumed"} {
		decisions = append(decisions, domain.RunEvent{ID: kind, Type: kind, NodeID: "review", Timestamp: at})
	}
	putRun("current", "sop", "", "", "succeeded", at, false, decisions)
	putRun("failed", "sop", "", "", "failed", at, false, nil)
	putRun("child", "sop", "", "current", "failed", at, false, decisions)
	putRun("group", "sop", "", "", "succeeded", at, true, decisions)
	putRun("space", "vibe", domain.RunKindWorkspace, "", "succeeded", at, false, nil)
	putRun("old-sop", "mixed", "", "", "succeeded", old, false, nil)
	putRun("old-space", "mixed", domain.RunKindWorkspace, "", "succeeded", old, false, nil)
	putRun("repeat-run", "repeat", "", "", "succeeded", old, false, nil)
	putRun("baseline-run", "baseline", "", "", "succeeded", old, false, nil)
	putRun("private-run", "other", "", "", "succeeded", at, false, decisions)
	for i, mr := range []struct {
		issue, branch string
		at            time.Time
	}{
		{"sop", "main", at}, {"sop", "main", at.Add(time.Hour)}, {"vibe", "main", at}, {"mixed", "main", at},
		{"repeat", "main", old}, {"repeat", "main", at}, {"baseline", "agentflow/integration", at}, {"unattributed", "main", at}, {"other", "main", at},
	} {
		if err := s.DB.Create(&mrDeliveryStateRow{MRProject: "dashboard/repo", MRIID: i + 1, IssueID: mr.issue, State: "merged", TargetBranch: mr.branch, MergedAt: timePtr(mr.at)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, session := range []domain.AssistantSession{
		{ID: "dashboard-chat", ProjectID: "dashboard", CreatedAt: at, Messages: []domain.AssistantMessage{{Role: "user", AuthorID: 7}, {Role: "user", AuthorID: 8}}},
		{ID: "dashboard-old-chat", ProjectID: "dashboard", CreatedAt: old, Messages: []domain.AssistantMessage{{Role: "user", AuthorID: 9}}},
		{ID: "sop-dashboard", ProjectID: "dashboard", CreatedAt: at, Shared: true, RunID: "current", Messages: []domain.AssistantMessage{{Role: "user", AuthorID: 10}}},
	} {
		if err := s.PutAssistantSession(7, session); err != nil {
			t.Fatal(err)
		}
	}
	filter := domain.AnalyticsFilter{Scope: "dashboard", ProjectID: "dashboard", From: at.Add(-30 * time.Minute), To: at.Add(8 * time.Hour), TimeZone: "Asia/Shanghai"}
	got := s.Analytics(filter)
	if len(got.DataErrors) > 0 || got.Dashboard == nil {
		t.Fatalf("dashboard: %+v", got.DataErrors)
	}
	d := got.Dashboard
	if len(got.RunTraces) != 0 || len(got.MRMerges) != 0 || len(got.UserUsage) != 0 {
		t.Fatal("compact dashboard must not return raw run, MR or per-member indexes")
	}
	if d.SOPRuns != 2 || d.SucceededRuns != 1 || d.FailedRuns != 1 || d.HumanRuns != 1 || d.P95ExecutionDurationMs != 180000 {
		t.Fatalf("SOP cohort: %+v", d)
	}
	if d.DeliveredIssues != 4 || d.SOPDeliveredIssues != 3 || d.VibeDeliveredIssues != 1 {
		t.Fatalf("merge deduplication / routes: %+v", d)
	}
	if d.ActiveMembers != 2 || got.Activity.Sessions != 1 || got.Activity.CollaborativeSessions != 1 {
		t.Fatalf("members/sessions: %+v, %+v", d, got.Activity)
	}
	if len(d.Trend) != 1 || d.Trend[0].Date != "2026-11-23" || len(got.ActivityTrend) != 1 || got.ActivityTrend[0].Date != "2026-11-23" {
		t.Fatalf("time zone: %+v / %+v", d.Trend, got.ActivityTrend)
	}
	if p := d.Trend[0]; p.SOPRuns != 2 || p.SucceededRuns != 1 || p.FailedRuns != 1 || p.HumanRuns != 1 {
		t.Fatalf("period quality and intervention rates must exclude child SOPs and deduplicate decisions: %+v", p)
	}
	if len(d.HumanTrend) != 1 || d.HumanTrend[0].Approvals != 4 || d.HumanTrend[0].Rejections != 2 || d.HumanTrend[0].Retries != 4 {
		t.Fatalf("human events: %+v", d.HumanTrend)
	}
	if len(d.HumanNodes) != 1 || d.HumanNodes[0].Name != "交付 · 代码评审" {
		t.Fatalf("nodes: %+v", d.HumanNodes)
	}
	var count int64
	s.DB.Model(&mrDeliveryStateRow{}).Count(&count)
	if count != 9 {
		t.Fatalf("analytics wrote MR state: %d", count)
	}
	filter.ProjectIDs = []string{}
	if empty := s.Analytics(filter); empty.Dashboard.SOPRuns != 0 || empty.Dashboard.DeliveredIssues != 0 || empty.Dashboard.ActiveMembers != 0 {
		t.Fatalf("empty scope: %+v", empty.Dashboard)
	}
	filter.ProjectIDs = nil
	filter.Origin = "external"
	if external := s.Analytics(filter); external.Dashboard.SOPRuns != 0 || external.Dashboard.DeliveredIssues != 0 || external.Dashboard.ActiveMembers != 0 {
		t.Fatalf("external scope: %+v", external.Dashboard)
	}
}

func TestAnalyticsDashboardNestedDecisionsAndReadFailure(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	at := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	for _, spec := range []struct{ id, parent, decision string }{{"root", "", ""}, {"child", "root", "gate.approved"}, {"grandchild", "child", "gate.rejected"}} {
		run := domain.Run{ID: spec.id, WorkflowID: "nested", Status: "succeeded", CreatedAt: at, UpdatedAt: at.Add(time.Minute), ExecutionContext: domain.TelemetryContext{ProjectID: "nested", ParentRunID: spec.parent}}
		if spec.decision != "" {
			run.Events = []domain.RunEvent{{ID: spec.id, NodeID: "review", Type: spec.decision, Timestamp: at}}
		}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	filter := domain.AnalyticsFilter{ProjectID: "nested", Scope: "dashboard"}
	got := s.Analytics(filter)
	if len(got.DataErrors) > 0 || got.Dashboard.SOPRuns != 1 || got.Dashboard.HumanRuns != 1 {
		t.Fatalf("nested cohort: %+v / %v", got.Dashboard, got.DataErrors)
	}
	if len(got.Dashboard.HumanTrend) != 1 || got.Dashboard.HumanTrend[0].Approvals != 1 || got.Dashboard.HumanTrend[0].Rejections != 1 {
		t.Fatalf("nested decisions: %+v", got.Dashboard.HumanTrend)
	}
	if err := s.DB.Migrator().DropTable(&mrDeliveryStateRow{}); err != nil {
		t.Fatal(err)
	}
	if got := s.Analytics(filter); len(got.DataErrors) == 0 {
		t.Fatal("failed delivery read must not masquerade as zero deliveries")
	}
}

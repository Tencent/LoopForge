package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsActivityCountsConversationsRequirementsAndSOPs(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	for _, issue := range []domain.Issue{
		{ID: "activity-req", ProjectID: "activity", Kind: "requirement", Source: "local", AssigneeUserID: 7, CreatedAt: at},
		{ID: "activity-no-run", ProjectID: "activity", Kind: "requirement", Source: "local", AssigneeUserID: 8, CreatedAt: at},
		{ID: "activity-bug", ProjectID: "activity", Kind: "issue", Source: "gongfeng", CreatedAt: at},
		{ID: "activity-other", ProjectID: "other", Kind: "requirement", CreatedAt: at},
	} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
		// PutIssue uses the server clock for a newly created record.
		if err := s.DB.Model(&issueRow{}).Where("id = ?", issue.ID).Updates(map[string]any{"created_at": at, "payload": mustJSON(issue)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range []domain.Run{
		{ID: "activity-sop-1", ExecutionContext: domain.TelemetryContext{ProjectID: "activity", IssueID: "activity-req", ResponsibleUserID: 7}},
		{ID: "activity-sop-2", ExecutionContext: domain.TelemetryContext{IssueID: "activity-req", ResponsibleUserID: 7}},
		{ID: "activity-group", WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group"}, ExecutionContext: domain.TelemetryContext{ProjectID: "activity", IssueID: "activity-req"}},
		{ID: "activity-child", ExecutionContext: domain.TelemetryContext{ProjectID: "activity", IssueID: "activity-req", ParentRunID: "activity-sop-1"}},
		{ID: "activity-ws", Kind: domain.RunKindWorkspace, ExecutionContext: domain.TelemetryContext{ProjectID: "activity", IssueID: "activity-req"}},
		{ID: "activity-bug-run", ExecutionContext: domain.TelemetryContext{ProjectID: "activity", IssueID: "activity-bug"}},
	} {
		run.CreatedAt = at
		run.Status = "succeeded"
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	for _, session := range []domain.AssistantSession{
		{ID: "activity-collab", ProjectID: "activity", IssueID: "activity-req", Archived: true, Messages: []domain.AssistantMessage{{Role: "user", AuthorID: 7}, {Role: "user", AuthorID: 8}, {Role: "user", AuthorID: 8}}},
		{ID: "activity-solo", ProjectID: "activity", Messages: []domain.AssistantMessage{{Role: "user", AuthorID: 7}, {Role: "assistant", AuthorID: 8}, {Role: "user", AuthorID: 8, Trigger: "cron"}, {Role: "user", AuthorID: 8, Content: "已确认并执行 草稿。请核对这条草稿的保存回执，继续完成原始请求"}}},
		{ID: "activity-other-chat", ProjectID: "other", Messages: []domain.AssistantMessage{{Role: "user", AuthorID: 7}, {Role: "user", AuthorID: 8}}},
		{ID: "sop-activity", ProjectID: "activity", Shared: true, RunID: "activity-sop-1", Messages: []domain.AssistantMessage{{Role: "user", AuthorID: 7}, {Role: "user", AuthorID: 8}}},
	} {
		session.CreatedAt = at
		if err := s.PutAssistantSession(7, session); err != nil {
			t.Fatal(err)
		}
	}
	// An older row has no participant cache. The read-only fallback must derive
	// the same two distinct humans and must never update its payload.
	legacy := domain.AssistantSession{ID: "activity-legacy", UserID: 9, ProjectID: "activity", CreatedAt: at.AddDate(0, 0, -1), Messages: []domain.AssistantMessage{{Role: "user", AuthorID: 9}, {Role: "user", AuthorID: 8}}}
	before := mustJSON(legacy)
	if err := s.DB.Create(&assistantSessionRow{ID: legacy.ID, UserID: 9, ProjectID: legacy.ProjectID, Payload: before, Created: timePtr(legacy.CreatedAt)}).Error; err != nil {
		t.Fatal(err)
	}
	filter := domain.AnalyticsFilter{ProjectID: "activity", Scope: "overview"}
	got := s.Analytics(filter)
	want := domain.AnalyticsActivity{Sessions: 3, CollaborativeSessions: 2, Requirements: 2, SOPRuns: 2,
		SessionUsage: domain.AnalyticsSessionUsage{MissingReplies: 1, UndatedReplies: 1}}
	if len(got.DataErrors) > 0 || got.Activity != want {
		t.Fatalf("activity=%+v errors=%v want=%+v", got.Activity, got.DataErrors, want)
	}
	if len(got.ActivityTrend) != 2 || got.ActivityTrend[1].Date != "2026-10-08" || got.ActivityTrend[1].Sessions != 2 {
		t.Fatalf("trend=%+v", got.ActivityTrend)
	}
	full := filter
	full.Scope = ""
	if a := s.Analytics(full).Activity; a != want {
		t.Fatalf("full differs: %+v", a)
	}
	for _, tc := range []struct {
		name   string
		filter domain.AnalyticsFilter
		want   domain.AnalyticsActivity
	}{
		{"participant", domain.AnalyticsFilter{ProjectID: "activity", UserID: "8"}, domain.AnalyticsActivity{Sessions: 2, CollaborativeSessions: 2, Requirements: 1}},
		{"window", domain.AnalyticsFilter{ProjectID: "activity", From: at, To: at.Add(time.Hour)}, domain.AnalyticsActivity{Sessions: 2, CollaborativeSessions: 1, Requirements: 2, SOPRuns: 2}},
		{"linked-source", domain.AnalyticsFilter{ProjectID: "activity", IssueSource: "local"}, domain.AnalyticsActivity{Sessions: 1, CollaborativeSessions: 1, Requirements: 2, SOPRuns: 2}},
		{"external", domain.AnalyticsFilter{ProjectID: "activity", Origin: "external"}, domain.AnalyticsActivity{}},
		{"no-projects", domain.AnalyticsFilter{ProjectIDs: []string{}}, domain.AnalyticsActivity{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := s.Analytics(tc.filter)
			if a.Activity != tc.want || len(a.DataErrors) > 0 {
				t.Fatalf("got=%+v errors=%v want=%+v", a.Activity, a.DataErrors, tc.want)
			}
		})
	}
	var after assistantSessionRow
	if err := s.DB.Where("id = ?", legacy.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if string(after.Payload) != string(before) {
		t.Fatal("analytics mutated legacy session")
	}
}

func TestAnalyticsActivityTokenUsageUsesReplyTimeAndExcludesInheritedHistory(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	sept := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	oct := sept.AddDate(0, 1, 7)
	nov := sept.AddDate(0, 2, 2)
	reply := func(at time.Time, total int64) domain.AssistantMessage {
		return domain.AssistantMessage{Role: "assistant", CreatedAt: at, Usage: &domain.TokenUsage{InputTokens: total - 10, OutputTokens: 10, TotalTokens: total}}
	}
	root := []domain.AssistantMessage{
		{Role: "user", AuthorID: 7, CreatedAt: oct}, reply(oct, 100),
		{Role: "user", AuthorID: 8, CreatedAt: oct}, {Role: "assistant", CreatedAt: oct, Content: "legacy reply without usage"},
		reply(nov, 50), {Role: "assistant", CreatedAt: oct, Usage: &domain.TokenUsage{}},
		{Role: "user", AuthorID: 7, CreatedAt: oct, Usage: &domain.TokenUsage{TotalTokens: 9999}},
	}
	root[1].Usage.CacheReadTokens = 200
	root[1].Usage.CacheWriteTokens = 10
	sessions := []domain.AssistantSession{
		{ID: "usage-root", UserID: 7, ProjectID: "usage", CreatedAt: sept, Messages: root},
		{ID: "usage-solo", UserID: 7, ProjectID: "usage", CreatedAt: oct, Messages: []domain.AssistantMessage{
			{Role: "user", AuthorID: 7}, {Role: "user", AuthorID: 8, Trigger: "cron"},
			{Role: "assistant", StartedAt: &oct, Usage: &domain.TokenUsage{InputTokens: 10, OutputTokens: 20}},
		}},
		{ID: "usage-fork", UserID: 9, ProjectID: "usage", CreatedAt: nov, ParentSessionID: "usage-root", ForkMessageIndex: 2, Messages: append(append([]domain.AssistantMessage{}, root[:2]...), domain.AssistantMessage{Role: "user", AuthorID: 9}, reply(nov, 70))},
		{ID: "usage-undated", UserID: 7, ProjectID: "usage", CreatedAt: sept, Messages: []domain.AssistantMessage{{Role: "user", AuthorID: 7}, reply(time.Time{}, 11)}},
		{ID: "usage-other", UserID: 7, ProjectID: "other", CreatedAt: oct, Messages: []domain.AssistantMessage{reply(oct, 9000)}},
		{ID: "sop-usage", UserID: 7, ProjectID: "usage", CreatedAt: oct, Shared: true, RunID: "sop", Messages: []domain.AssistantMessage{reply(oct, 9000)}},
	}
	for _, session := range sessions {
		if err := s.PutAssistantSession(session.UserID, session); err != nil {
			t.Fatal(err)
		}
	}
	filter := domain.AnalyticsFilter{ProjectID: "usage", Scope: "overview", Grain: "month"}
	got := s.Analytics(filter)
	if len(got.DataErrors) > 0 {
		t.Fatal(got.DataErrors)
	}
	usage := got.Activity.SessionUsage
	if usage.Tokens != 261 || usage.ReportedReplies != 6 || usage.MissingReplies != 1 || usage.UndatedReplies != 1 || usage.CacheReadTokens != 200 || usage.CacheWriteTokens != 10 {
		t.Fatalf("total usage=%+v", usage)
	}
	collab := got.Activity.CollaborativeUsage
	if collab.Tokens != 220 || collab.ReportedReplies != 4 || collab.MissingReplies != 1 {
		t.Fatalf("collaborative usage=%+v", collab)
	}
	// Usage is a separate view of stored replies, not another charge to Run totals.
	if got.Summary.TotalTokens != 0 {
		t.Fatalf("session usage was added to Run total: %+v", got.Summary)
	}
	for _, p := range got.ActivityTrend {
		if p.Date == "2026-10" && (p.SessionUsage.Tokens != 130 || p.CollaborativeUsage.Tokens != 100 || p.SessionUsage.MissingReplies != 1) {
			t.Fatalf("October=%+v", p)
		}
		if p.Date == "2026-11" && (p.SessionUsage.Tokens != 120 || p.CollaborativeUsage.Tokens != 120) {
			t.Fatalf("November=%+v", p)
		}
	}
	filter.From = nov.AddDate(0, 0, -2)
	filter.To = nov.AddDate(0, 1, 0)
	window := s.Analytics(filter).Activity
	if window.Sessions != 1 || window.SessionUsage.Tokens != 120 || window.CollaborativeUsage.Tokens != 120 || window.SessionUsage.UndatedReplies != 0 {
		t.Fatalf("old conversation usage lost from new window: %+v", window)
	}
	filter.From = time.Time{}
	filter.To = time.Time{}
	filter.UserID = "9"
	personal := s.Analytics(filter).Activity
	if personal.Sessions != 1 || personal.SessionUsage.Tokens != 70 || personal.CollaborativeUsage.Tokens != 70 {
		t.Fatalf("personal fork usage=%+v", personal)
	}
	// Exercise the MySQL 5.7 fallback against the same portable JSON extraction.
	previous := noAnalyticsUsageProjection.Swap(true)
	t.Cleanup(func() { noAnalyticsUsageProjection.Store(previous) })
	filter.UserID = ""
	fallback := s.Analytics(filter)
	if len(fallback.DataErrors) > 0 || fallback.Activity != got.Activity {
		t.Fatalf("fallback=%+v errors=%v", fallback.Activity, fallback.DataErrors)
	}
}

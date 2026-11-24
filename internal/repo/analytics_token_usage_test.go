package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsTokenUsageReconcilesMembersModelsAndDays(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	at := time.Date(2026, 11, 22, 16, 30, 0, 0, time.UTC)
	runs := []domain.Run{
		{ID: "tokens-a", Status: "succeeded", CreatedAt: at, UpdatedAt: at.Add(time.Minute), ExecutionContext: domain.TelemetryContext{ProjectID: "tokens", ResponsibleUserID: 7}, Usage: domain.TokenUsage{TotalTokens: 100}, Nodes: map[string]*domain.NodeRun{
			"a": {Model: "model-a", Usage: domain.TokenUsage{TotalTokens: 60}}, "b": {Model: "model-b", Usage: domain.TokenUsage{TotalTokens: 40}},
		}},
		{ID: "tokens-workspace", Kind: domain.RunKindWorkspace, Status: "running", CreatedAt: at, UpdatedAt: at.AddDate(0, 0, 1), ExecutionContext: domain.TelemetryContext{ProjectID: "tokens", ResponsibleUserID: 7}, Usage: domain.TokenUsage{TotalTokens: 20}},
		{ID: "tokens-b", Status: "succeeded", CreatedAt: at, UpdatedAt: at.AddDate(0, 0, 1), ExecutionContext: domain.TelemetryContext{ProjectID: "tokens", ResponsibleUserID: 8}, Usage: domain.TokenUsage{TotalTokens: 200}, Nodes: map[string]*domain.NodeRun{
			"b": {Model: "model-b", Usage: domain.TokenUsage{TotalTokens: 200}},
		}},
		{ID: "tokens-other", Status: "succeeded", CreatedAt: at, UpdatedAt: at, ExecutionContext: domain.TelemetryContext{ProjectID: "other", ResponsibleUserID: 9}, Usage: domain.TokenUsage{TotalTokens: 900}},
	}
	for _, run := range runs {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		user string
		want int64
	}{{"", 320}, {"7", 120}, {"8", 200}} {
		out := s.Analytics(domain.AnalyticsFilter{ProjectID: "tokens", UserID: tc.user, Scope: "overview", IncludeDashboard: true, Grain: "month", TimeZone: "Asia/Shanghai"})
		if len(out.DataErrors) != 0 {
			t.Fatal(out.DataErrors)
		}
		var total int64
		byDayMember := map[string]int64{}
		for _, row := range out.TokenUsage {
			total += row.Tokens
			byDayMember[row.Date+":"+row.UserID] += row.Tokens
			if tc.user != "" && row.UserID != tc.user {
				t.Fatalf("personal model usage leaked another member: %+v", row)
			}
		}
		if total != tc.want || total != out.Summary.TotalTokens {
			t.Fatalf("token split must match summary: %d / %d / %d", total, tc.want, out.Summary.TotalTokens)
		}
		for _, row := range out.UserUsage {
			if row.Tokens != byDayMember[row.Date+":"+row.UserID] {
				t.Fatalf("model and member views disagree on daily usage: %+v / %+v", row, out.TokenUsage)
			}
		}
		if tc.user == "7" {
			if len(out.TokenUsage) != 3 || out.TokenUsage[0].Model != "" || out.TokenUsage[0].Tokens != 20 || out.TokenUsage[0].Date != "2026-11-22" {
				t.Fatalf("workspace tokens must remain visible without inventing a model: %+v", out.TokenUsage)
			}
		}
	}
	if legacy := s.Analytics(domain.AnalyticsFilter{ProjectID: "tokens", Scope: "overview"}); len(legacy.TokenUsage) != 0 {
		t.Fatal("legacy analytics should not allocate additional member/model indexes")
	}
	if compact := s.Analytics(domain.AnalyticsFilter{ProjectID: "tokens", Scope: "dashboard", IncludeDashboard: true}); len(compact.TokenUsage) != 0 {
		t.Fatal("compact API must keep its no-member-index contract")
	}
}

func TestAnalyticsTokenUsageUnknownAndExternalAttribution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		nodes    map[string]*domain.NodeRun
		external []domain.InsightUsage
		want     map[string]int64
	}{
		{"partial", map[string]*domain.NodeRun{"a": {Model: "model-a", Usage: domain.TokenUsage{TotalTokens: 30}}}, nil, map[string]int64{"model-a": 30, "": 70}},
		{"conflicting", map[string]*domain.NodeRun{"a": {Model: "model-a", Usage: domain.TokenUsage{TotalTokens: 120}}}, nil, map[string]int64{"": 100}},
		{"external", nil, []domain.InsightUsage{{Model: "model-a", InputTokens: 50, OutputTokens: 10, CacheReadTokens: 40}, {Model: "model-b", InputTokens: 30, OutputTokens: 10}}, map[string]int64{"model-a": 60, "model-b": 40}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[analyticsTokenUsageKey]int64{}
			accumulateAnalyticsTokenUsage(values, domain.Run{Usage: domain.TokenUsage{TotalTokens: 100}, Nodes: tc.nodes}, "", "2026-11-22", tc.external)
			var total int64
			for _, row := range finalizeAnalyticsTokenUsage(values) {
				total += row.Tokens
				if row.UserID != "unassigned" || row.Tokens != tc.want[row.Model] {
					t.Fatalf("model attribution: %+v", row)
				}
			}
			if total != 100 || len(values) != len(tc.want) {
				t.Fatalf("attribution must neither drop nor add tokens: %+v", values)
			}
		})
	}
}

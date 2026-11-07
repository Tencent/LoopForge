package repo

import (
	"agentflow/internal/domain"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestInsightIdempotencyAndAnalytics(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	source := domain.InsightSource{ID: "s1", ProjectID: "alpha", UserMappings: map[string]int64{"alice": 7}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateInsightSource(source, "hash1"); err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	active := int64(1500)
	v := domain.InsightExecution{ID: "event1", Type: "workflow_execution", ExternalExecutionID: "exec1", ExternalUserID: "alice", Workflow: domain.InsightWorkflow{ID: "flow", Name: "External flow"}, Status: "succeeded", StartedAt: now.Add(-time.Hour), FinishedAt: now, ExecutionDurationMs: &active, Usage: []domain.InsightUsage{{Provider: "p", Model: "glm-5.3", InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30, CostStatus: "reported", CostUSD: &zero}, {Provider: "p", Model: "m2", InputTokens: 50, OutputTokens: 10, CostStatus: "unknown"}}}
	var wg sync.WaitGroup
	accepted := make(chan bool, 12)
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			duplicate, err := s.IngestInsightExecution(source, v)
			if err != nil {
				failures <- err
			}
			accepted <- !duplicate
		}()
	}
	wg.Wait()
	close(accepted)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	count := 0
	for fresh := range accepted {
		if fresh {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("accepted %d times", count)
	}
	changed := v
	changed.Status = "failed"
	if _, err := s.IngestInsightExecution(source, changed); !errors.Is(err, ErrInsightConflict) {
		t.Fatalf("conflict: %v", err)
	}
	changed = v
	changed.ID = "another-event"
	if _, err := s.IngestInsightExecution(source, changed); !errors.Is(err, ErrInsightConflict) {
		t.Fatalf("execution dedup: %v", err)
	}
	if err := s.PutRun(domain.Run{ID: "native", WorkflowID: "native", Status: "failed", CreatedAt: now, UpdatedAt: now.Add(time.Second), ExecutionContext: domain.TelemetryContext{ProjectID: "alpha", ResponsibleUserID: 7}, Usage: domain.TokenUsage{InputTokens: 10, TotalTokens: 10, CostStatus: "reported"}}); err != nil {
		t.Fatal(err)
	}
	// PutRun stamps UpdatedAt on write; pin the native fixture completion time.
	if err := s.DB.Model(&runRow{}).Where("id = ?", "native").Update("updated_at", now.Add(time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"overview", ""} {
		got := s.Analytics(domain.AnalyticsFilter{ProjectID: "alpha", Scope: scope})
		// Unknown models and missing native model/token attribution cannot be
		// priced from public tariffs, even if an adapter reported zero.
		if len(got.DataErrors) > 0 || got.Summary.TotalRuns != 2 || got.Summary.TotalTokens != 190 || got.Summary.ExternalRuns != 1 || got.Summary.CostUnknownRuns != 2 {
			t.Fatalf("summary: %+v", got.Summary)
		}
		delta := got.Summary.TotalCostCNY - .00118 // inclusive input: 70*8 + 30*2 + 20*28 per million
		if delta < -1e-12 || delta > 1e-12 {
			t.Fatalf("external public CNY estimate: %+v", got.Summary)
		}
		if got.Demo.AutomatedSucceededRuns != 0 || got.Demo.CoveredRuns != 1 || got.Summary.FirstPassRate != 0 || got.Summary.UniqueIssues != 0 {
			t.Fatalf("invented quality/requirements: %+v %+v", got.Demo, got.Summary)
		}
		if len(got.Models) != 2 || len(got.Nodes) != 0 || len(got.UserUsage) != 1 || got.UserUsage[0].Tokens != 190 || len(got.ProjectUsage) != 1 || got.ProjectUsage[0].CostKnown {
			t.Fatalf("breakdowns: %+v", got)
		}
		if len(got.Trend) != 1 || got.Trend[0].Tokens != 190 || got.Trend[0].Date != "2026-10-08" {
			t.Fatalf("completion date: %+v", got.Trend)
		}
	}
	for _, tc := range []struct {
		filter domain.AnalyticsFilter
		want   int
	}{
		{domain.AnalyticsFilter{Origin: "external"}, 1}, {domain.AnalyticsFilter{Origin: "internal"}, 1},
		{domain.AnalyticsFilter{ProjectID: "beta"}, 0}, {domain.AnalyticsFilter{ProjectIDs: []string{}}, 0}, {domain.AnalyticsFilter{ProjectIDs: []string{"beta"}}, 0},
		{domain.AnalyticsFilter{UserID: "8"}, 0}, {domain.AnalyticsFilter{Origin: "external", From: now, To: now}, 1},
		{domain.AnalyticsFilter{Origin: "external", To: now.Add(-time.Second)}, 0}, {domain.AnalyticsFilter{Origin: "external", IssueSource: "local"}, 0},
		{domain.AnalyticsFilter{RequirementsOnly: true}, 0},
	} {
		got := s.Analytics(tc.filter)
		if got.Summary.TotalRuns != tc.want {
			t.Fatalf("filter %+v: %d want %d", tc.filter, got.Summary.TotalRuns, tc.want)
		}
	}
	var stored int64
	s.DB.Model(&runRow{}).Count(&stored)
	if stored != 1 {
		t.Fatalf("external persisted as Run: %d", stored)
	}
	// Revocation blocks writes but retains history.
	if err := s.RevokeInsightSource(source.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestInsightExecution(source, v); err == nil {
		t.Fatal("revoked source accepted")
	}
	if got := s.Analytics(domain.AnalyticsFilter{Origin: "external"}); got.Summary.TotalRuns != 1 {
		t.Fatal("revocation erased history")
	}
}

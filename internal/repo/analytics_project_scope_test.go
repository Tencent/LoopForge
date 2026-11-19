package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsUnassignedRunsRespectLegacyProjectsAndPrefetch(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutIssue(domain.Issue{ID: "scoped-issue", ProjectID: "p1", Kind: "requirement"}); err != nil {
		t.Fatal(err)
	}
	runs := []domain.Run{
		{ID: "unassigned", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 77}},
		{ID: "stamped", ExecutionContext: domain.TelemetryContext{ProjectID: "p1", ResponsibleUserID: 77}},
		{ID: "legacy", ExecutionContext: domain.TelemetryContext{IssueID: "scoped-issue", ResponsibleUserID: 77}},
		{ID: "other-user", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 88}},
	}
	for i := range runs {
		runs[i].Status, runs[i].CreatedAt = "succeeded", time.Now().UTC()
		if err := s.PutRun(runs[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []string{"overview", ""} {
		filter := domain.AnalyticsFilter{ProjectScope: "unassigned", UserID: "77", Scope: scope}
		for _, prefetched := range [][]domain.Run{nil, runs} {
			got := s.analyticsWithRuns(filter, prefetched)
			if len(got.DataErrors) != 0 || got.Summary.TotalRuns != 1 || len(got.RunTraces) != 1 || got.RunTraces[0].RunID != "unassigned" {
				t.Fatalf("scope=%q prefetch=%v: summary=%+v traces=%+v errors=%v", scope, prefetched != nil, got.Summary, got.RunTraces, got.DataErrors)
			}
		}
	}
}

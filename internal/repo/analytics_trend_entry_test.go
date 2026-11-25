package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestAnalyticsTrendEntryReusesBucketAndSetsDate(t *testing.T) {
	trends := map[string]*domain.AnalyticsTrendPoint{}
	first := analyticsTrendEntry(trends, "2026-11-14")
	if first == nil || first.Date != "2026-11-14" {
		t.Fatalf("unexpected new trend point: %+v", first)
	}
	first.Runs++
	if again := analyticsTrendEntry(trends, "2026-11-14"); again != first || again.Runs != 1 {
		t.Fatalf("expected the same trend point for the same bucket, got %+v", again)
	}
	if other := analyticsTrendEntry(trends, "2026-11-15"); other == first || other.Date != "2026-11-15" {
		t.Fatalf("expected a new trend point for another bucket, got %+v", other)
	}
	if len(trends) != 2 {
		t.Fatalf("expected 2 trend points, got %d", len(trends))
	}
}

func TestAddAnalyticsTrendIssueSkipsEmptyAndDeduplicates(t *testing.T) {
	trendIssues := map[string]map[string]struct{}{}
	addAnalyticsTrendIssue(trendIssues, "2026-11", "")
	if len(trendIssues) != 0 {
		t.Fatalf("empty issue ID must not create a bucket set, got %v", trendIssues)
	}
	addAnalyticsTrendIssue(trendIssues, "2026-11", "issue-1")
	addAnalyticsTrendIssue(trendIssues, "2026-11", "issue-1")
	addAnalyticsTrendIssue(trendIssues, "2026-11", "issue-2")
	addAnalyticsTrendIssue(trendIssues, "2026-12", "issue-1")
	if got := len(trendIssues["2026-11"]); got != 2 {
		t.Fatalf("expected 2 distinct issues in 2026-11, got %d", got)
	}
	if got := len(trendIssues["2026-12"]); got != 1 {
		t.Fatalf("expected 1 issue in 2026-12, got %d", got)
	}
}

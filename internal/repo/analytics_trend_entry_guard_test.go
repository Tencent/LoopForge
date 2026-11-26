package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// 对话工作区的趋势点与需求集合必须按 grain 分桶，而不是固定按天。
func TestAccumulateConversationRunUsesGrainBucket(t *testing.T) {
	cases := []struct {
		grain  string
		bucket string
	}{
		{grain: "month", bucket: "2026-11"},
		{grain: "week", bucket: "2026-W46"},
		{grain: "day", bucket: ""},
	}
	for _, tc := range cases {
		t.Run("grain="+tc.grain, func(t *testing.T) {
			summary := domain.AnalyticsSummary{}
			issues := map[string]struct{}{}
			trends := map[string]*domain.AnalyticsTrendPoint{}
			trendIssues := map[string]map[string]struct{}{}
			users := map[string]*analyticsUserAccumulator{}
			projects := map[string]*analyticsProjectAccumulator{}
			sources := map[string]*analyticsAccumulator{}
			runs := []domain.Run{
				{ID: "conv-1", CreatedAt: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC), Usage: domain.TokenUsage{TotalTokens: 100}},
				{ID: "conv-2", CreatedAt: time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC), Usage: domain.TokenUsage{TotalTokens: 50}},
			}
			for i := range runs {
				runs[i].ExecutionContext.IssueID = "issue-1"
				accumulateConversationRun(&summary, issues, trends, trendIssues, users, projects, sources, runs[i], domain.Issue{ID: "issue-1"}, tc.grain)
			}
			if tc.bucket == "" {
				// 按天粒度：两天各一个点，每个桶各登记一次同一需求。
				if len(trends) != 2 || trends["2026-11-10"] == nil || trends["2026-11-12"] == nil {
					t.Fatalf("day grain trends = %v", trends)
				}
				if len(trendIssues) != 2 || len(trendIssues["2026-11-10"]) != 1 || len(trendIssues["2026-11-12"]) != 1 {
					t.Fatalf("day grain trend issues = %v", trendIssues)
				}
				return
			}
			point := trends[tc.bucket]
			if len(trends) != 1 || point == nil {
				t.Fatalf("expected a single %q trend point, got %v", tc.bucket, trends)
			}
			if point.Date != tc.bucket || point.ConversationWorkspaces != 2 || point.ConversationTokens != 150 || point.Tokens != 150 {
				t.Fatalf("trend point = %+v", point)
			}
			if len(trendIssues) != 1 || len(trendIssues[tc.bucket]) != 1 {
				t.Fatalf("expected issue-1 once under %q, got %v", tc.bucket, trendIssues)
			}
			if _, ok := trendIssues[tc.bucket]["issue-1"]; !ok {
				t.Fatalf("issue-1 missing from %q: %v", tc.bucket, trendIssues)
			}
		})
	}
}

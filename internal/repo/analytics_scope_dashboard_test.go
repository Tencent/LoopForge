package repo

import (
	"math"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsGroupedDashboardHonorsPersonalAndProjectScope(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	at := time.Date(2026, 11, 22, 16, 30, 0, 0, time.UTC)
	for i, actor := range []int64{7, 8} {
		id := []string{"scope-a", "scope-b"}[i]
		if err := s.PutIssue(domain.Issue{ID: id, ProjectID: "scope", Kind: "requirement", Source: "local", AssigneeUserID: actor}); err != nil {
			t.Fatal(err)
		}
		for _, workspace := range []bool{false, true} {
			runID, kind, cost, tokens := id+"-run", "", float64(i+1), int64((i+1)*100)
			if workspace {
				runID = id + "-workspace"
				kind = domain.RunKindWorkspace
				cost /= 10
				tokens /= 10
			}
			run := domain.Run{ID: runID, Kind: kind, WorkflowID: id, Status: "succeeded", CreatedAt: at, UpdatedAt: at.Add(time.Minute), ExecutionContext: domain.TelemetryContext{ProjectID: "scope", IssueID: id, ResponsibleUserID: actor}, Usage: domain.TokenUsage{TotalTokens: tokens, CostUSD: cost, CostStatus: "reported"}}
			if !workspace {
				run.Events = []domain.RunEvent{{ID: runID, NodeID: id, Type: "gate.approved", Timestamp: at}}
			}
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.DB.Create(&mrDeliveryStateRow{MRProject: "scope/repo", MRIID: i + 1, IssueID: id, State: "merged", TargetBranch: "main", MergedAt: timePtr(at.Add(time.Hour))}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []struct {
		id       string
		owner    int64
		messages []domain.AssistantMessage
	}{
		{"scope-chat-a", 7, []domain.AssistantMessage{{Role: "user", AuthorID: 7}, {Role: "user", AuthorID: 8}}},
		{"scope-chat-b", 8, []domain.AssistantMessage{{Role: "user", AuthorID: 8}}},
	} {
		if err := s.PutAssistantSession(spec.owner, domain.AssistantSession{ID: spec.id, ProjectID: "scope", CreatedAt: at, Messages: spec.messages}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		user                   string
		runs, sessions, spaces int
		cost                   float64
	}{
		{"7", 1, 1, 1, 7.7}, {"8", 1, 2, 1, 15.4}, {"", 2, 2, 2, 23.1},
	} {
		got := s.Analytics(domain.AnalyticsFilter{ProjectID: "scope", UserID: tc.user, Scope: "overview", IncludeDashboard: true, TimeZone: "Asia/Shanghai"})
		if len(got.DataErrors) > 0 || got.Dashboard == nil {
			t.Fatalf("scope %q: %v", tc.user, got.DataErrors)
		}
		if got.Dashboard.SOPRuns != tc.runs || got.Dashboard.HumanRuns != tc.runs || got.Dashboard.DeliveredIssues != tc.runs || len(got.RunTraces) != tc.runs {
			t.Fatalf("scope %q: %+v / traces=%d", tc.user, got.Dashboard, len(got.RunTraces))
		}
		if got.Activity.Sessions != tc.sessions || got.Summary.ConversationWorkspaces != tc.spaces || math.Abs(got.Summary.TotalCostCNY-tc.cost) > 1e-8 {
			t.Fatalf("scope %q: sessions=%d workspaces=%d cost=%f", tc.user, got.Activity.Sessions, got.Summary.ConversationWorkspaces, got.Summary.TotalCostCNY)
		}
		if len(got.Dashboard.Trend) != 1 || got.Dashboard.Trend[0].Date != "2026-11-22" || got.ActivityTrend[0].Date != "2026-11-22" {
			t.Fatalf("embedded buckets must match existing overview: %+v / %+v", got.Dashboard.Trend, got.ActivityTrend)
		}
		if p := got.Dashboard.Trend[0]; p.SOPRuns != tc.runs || p.SucceededRuns != tc.runs || p.HumanRuns != tc.runs {
			t.Fatalf("period cohorts must follow personal/project scope: %+v", p)
		}
		if tc.user == "7" && (len(got.Dashboard.HumanNodes) != 1 || got.Dashboard.HumanNodes[0].Key != "scope-a/scope-a") {
			t.Fatalf("personal node scope: %+v", got.Dashboard.HumanNodes)
		}
	}
}

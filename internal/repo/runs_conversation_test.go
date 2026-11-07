package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestConversationWorkspaceRunsInAnalytics(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			var s *Store
			var err error
			if driver == "mysql" {
				dsn := os.Getenv("AGENTFLOW_BUSINESS_DELIVERY_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("local isolated MySQL required")
				}
				s, err = Open(dsn)
			} else {
				s, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			checkConversationWorkspaceRunsInAnalytics(t, s)
		})
	}
}

func checkConversationWorkspaceRunsInAnalytics(t *testing.T, s *Store) {
	prefix := fmt.Sprintf("conv-ws-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, table := range []string{"run_nodes", "run_events", "runs", "issues", "projects", "mr_delivery_states"} {
			column := "id"
			if table == "mr_delivery_states" {
				column = "mr_project"
			} else if table == "run_nodes" || table == "run_events" {
				column = "run_id"
			}
			s.DB.Table(table).Where(column+" LIKE ?", prefix+"%").Delete(nil)
		}
	})
	if err := s.PutProject(domain.Project{ID: prefix, Name: "Conversation"}); err != nil {
		t.Fatal(err)
	}
	mrProject := prefix + "/web"
	mrURL := "https://git.example.com/" + mrProject + "/merge_requests/7"
	for _, issue := range []domain.Issue{
		{ID: prefix + "-sop", ProjectID: prefix, Title: "SOP 需求", Status: "todo", Kind: "requirement", Source: "local"},
		{ID: prefix + "-conv", ProjectID: prefix, Title: "对话需求", Status: "in_review", Kind: "requirement", Source: "local", AutomationState: "ignored", DeliveryMRs: []domain.DeliveryMR{{Project: mrProject, IID: 7, URL: mrURL, State: "opened"}}},
	} {
		if err := s.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	workspace := func(id string) domain.Run {
		return domain.Run{ID: prefix + "-" + id, Kind: domain.RunKindWorkspace, WorkflowID: domain.ConversationWorkspaceWorkflowID, Status: "waiting", Input: "对话改代码：" + id, CreatedAt: at,
			WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, Name: "对话工作区"}, ExecutionContext: domain.TelemetryContext{ProjectID: prefix, ResponsibleUserID: 7}, Nodes: map[string]*domain.NodeRun{}}
	}
	sop := domain.Run{ID: prefix + "-run", WorkflowID: "wf", Status: "succeeded", CreatedAt: at, WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Name: "SOP"},
		ExecutionContext: domain.TelemetryContext{ProjectID: prefix, IssueID: prefix + "-sop"}, Usage: domain.TokenUsage{TotalTokens: 1000, InputTokens: 900, OutputTokens: 100},
		Nodes: map[string]*domain.NodeRun{"dev": {Status: "succeeded", StartedAt: at, FinishedAt: at.Add(time.Minute), Usage: domain.TokenUsage{TotalTokens: 1000, InputTokens: 900, OutputTokens: 100}}}}
	for _, run := range []domain.Run{sop, workspace("linked"), workspace("review"), workspace("idle")} {
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	linked := prefix + "-linked"
	stale, _ := s.RunState(linked)
	if ok, err := s.LinkConversationRunIssue(linked, prefix+"-conv"); err != nil || !ok {
		t.Fatalf("link = %v %v", ok, err)
	}
	if ok, err := s.LinkConversationRunIssue(linked, prefix+"-sop"); err != nil || ok {
		t.Fatalf("a linked workspace must keep its requirement: %v %v", ok, err)
	}
	if ok, err := s.LinkConversationRunIssue(linked, " "); err != nil || ok {
		t.Fatalf("empty issue: %v %v", ok, err)
	}
	if ok, err := s.LinkConversationRunIssue(sop.ID, prefix+"-conv"); err != nil || ok {
		t.Fatalf("an SOP run must not be relinked: %v %v", ok, err)
	}
	if ok, err := s.LinkConversationRunIssue(prefix+"-missing", prefix+"-conv"); err != nil || ok {
		t.Fatalf("missing run: %v %v", ok, err)
	}
	for _, usage := range []domain.TokenUsage{{InputTokens: 200, OutputTokens: 40, TotalTokens: 240}, {InputTokens: 50, OutputTokens: 10, TotalTokens: 60}} {
		if err := s.AddConversationRunUsage(linked, "claude-sonnet-4", usage); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddConversationRunUsage(prefix+"-review", "unpriced-model", domain.TokenUsage{InputTokens: 40, OutputTokens: 10, TotalTokens: 50}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddConversationRunUsage(prefix+"-idle", "claude-sonnet-4", domain.TokenUsage{}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddConversationRunUsage(sop.ID, "claude-sonnet-4", domain.TokenUsage{TotalTokens: 999}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RunState(sop.ID); got.Usage.TotalTokens != 1000 {
		t.Fatalf("SOP usage changed: %+v", got.Usage)
	}
	// An engine write from a copy read before the conversation updates keeps them.
	stale.WorkspaceEditorID = 9
	if err := s.PutRun(stale); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(linked)
	if got.ExecutionContext.IssueID != prefix+"-conv" || got.Usage.TotalTokens != 300 || got.Usage.CostStatus != "estimated" || got.Usage.CostUSD <= 0 {
		t.Fatalf("stale write lost conversation fields: issue=%q usage=%+v", got.ExecutionContext.IssueID, got.Usage)
	}
	if review, _ := s.RunState(prefix + "-review"); review.Usage.CostStatus != "unknown" || review.Usage.TotalTokens != 50 {
		t.Fatalf("unpriced usage = %+v", review.Usage)
	}
	if err := s.EnsureMRDeliveryState(mrProject, 7, mrURL, prefix+"-conv"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMRDeliveryState(mrProject, 7, "merged", "master", at.Add(30*time.Minute), at.Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}

	filter := domain.AnalyticsFilter{ProjectID: prefix}
	out := s.Analytics(filter)
	sum := out.Summary
	if sum.TotalRuns != 1 || sum.SucceededRuns != 1 || sum.WaitingRuns != 0 || sum.QualityRuns != 1 {
		t.Fatalf("conversation workspaces must not count as SOP runs: %+v", sum)
	}
	if sum.ConversationWorkspaces != 3 || sum.ConversationLinkedWorkspaces != 1 || sum.ConversationIssues != 1 || sum.ConversationTokens != 350 || sum.TotalTokens != 1350 || sum.ConversationCostCNY <= 0 {
		t.Fatalf("conversation totals = workspaces %d linked %d issues %d tokens %d total %d cost %v", sum.ConversationWorkspaces, sum.ConversationLinkedWorkspaces, sum.ConversationIssues, sum.ConversationTokens, sum.TotalTokens, sum.ConversationCostCNY)
	}
	if sum.ConversationIssuesWithMR != 1 || sum.ConversationIssuesDelivered != 1 {
		t.Fatalf("conversation requirements with MR=%d delivered=%d", sum.ConversationIssuesWithMR, sum.ConversationIssuesDelivered)
	}
	if sum.UniqueIssues != 1 {
		t.Fatalf("SOP requirements must not include conversation ones: %d", sum.UniqueIssues)
	}
	if out.Demo.AverageTokensPerRun != 1000 {
		t.Fatalf("per-run average must stay SOP-only: %v", out.Demo.AverageTokensPerRun)
	}
	if len(out.RunTraces) != 1 || len(out.Workflows) != 1 || out.Workflows[0].Key != "wf" {
		t.Fatalf("SOP breakdowns polluted: traces=%d workflows=%+v", len(out.RunTraces), out.Workflows)
	}
	trendTokens, trendRuns, trendConversation, trendConversationCost := int64(0), 0, int64(0), 0.0
	trendIssues, trendConversationIssues, trendWorkspaces := 0, 0, 0
	for _, point := range out.Trend {
		trendTokens += point.Tokens
		trendRuns += point.Runs
		trendConversation += point.ConversationTokens
		trendConversationCost += point.ConversationCostCNY
		trendIssues += point.Issues
		trendConversationIssues += point.ConversationIssues
		trendWorkspaces += point.ConversationWorkspaces
	}
	if trendTokens != 1350 || trendRuns != 1 || trendConversation != 350 || trendConversationCost <= 0 {
		t.Fatalf("trend tokens=%d runs=%d conversation=%d cost=%v", trendTokens, trendRuns, trendConversation, trendConversationCost)
	}
	if trendIssues != 1 || trendConversationIssues != 1 || trendWorkspaces != 3 {
		t.Fatalf("trend SOP issues=%d conversation issues=%d workspaces=%d", trendIssues, trendConversationIssues, trendWorkspaces)
	}
	if len(out.ProjectUsage) != 1 || out.ProjectUsage[0].Tokens != 1350 || out.ProjectUsage[0].UniqueIssues != 2 || out.ProjectUsage[0].Runs != 1 {
		t.Fatalf("project usage = %+v", out.ProjectUsage)
	}
	userTokens := int64(0)
	for _, u := range out.UserUsage {
		userTokens += u.Tokens
	}
	if userTokens != 1350 {
		t.Fatalf("user usage tokens = %d", userTokens)
	}
	var conv *domain.AnalyticsDelivery
	for i := range out.Deliveries {
		if out.Deliveries[i].IssueID == prefix+"-conv" {
			conv = &out.Deliveries[i]
		}
	}
	if conv == nil || !conv.DirectDelivery || conv.SOPSucceeded || !conv.Delivered || conv.MRState != "merged" || conv.Tokens != 300 || conv.RunCount != 0 || conv.WorkflowName != "对话改代码" || conv.Title != "对话需求" || conv.DurationMs != 0 {
		t.Fatalf("conversation delivery = %+v", conv)
	}
	if d := out.Delivery; d.Issues != 2 || d.DirectIssues != 1 || d.DirectWithMR != 1 || d.DirectDelivered != 1 || d.SOPSucceeded != 1 {
		t.Fatalf("delivery = %+v", d)
	}
	overview := s.Analytics(domain.AnalyticsFilter{ProjectID: prefix, Scope: "overview"})
	if overview.Summary.ConversationIssuesWithMR != 1 || overview.Summary.ConversationIssuesDelivered != 1 {
		t.Fatalf("overview must carry the conversation requirement counts: %+v", overview.Summary)
	}
	if overview.Delivery.Issues != 2 || overview.Summary.ConversationTokens != 350 || overview.Summary.TotalRuns != 1 {
		t.Fatalf("overview = delivery %d tokens %d runs %d", overview.Delivery.Issues, overview.Summary.ConversationTokens, overview.Summary.TotalRuns)
	}

	// An SOP run of the same requirement remains its latest delivery run.
	later := sop
	later.ID, later.CreatedAt = prefix+"-run-conv", at.Add(-time.Minute)
	later.ExecutionContext.IssueID = prefix + "-conv"
	if err := s.PutRun(later); err != nil {
		t.Fatal(err)
	}
	for _, d := range s.Analytics(filter).Deliveries {
		if d.IssueID == prefix+"-conv" && (d.RunID != later.ID || d.DirectDelivery || !d.SOPSucceeded || d.RunCount != 1 || d.Tokens != 1300) {
			t.Fatalf("mixed delivery = %+v", d)
		}
	}
}

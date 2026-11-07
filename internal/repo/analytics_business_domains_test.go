package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestBusinessDomainDeliveryBatchesAndGormColumns(t *testing.T) {
	var s *Store
	var err error
	if dsn := os.Getenv("AGENTFLOW_BUSINESS_REVIEW_MYSQL_DSN"); dsn != "" {
		s, err = Open(dsn)
	} else {
		s, err = OpenTest()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	rows := make([]issueRow, 0, 501)
	deliveries := make([]domain.AnalyticsDelivery, 0, 504)
	for i := 0; i < 501; i++ {
		projectID := "review-visible"
		if i == 500 {
			projectID = "review-hidden"
		}
		id := fmt.Sprintf("review-batch-%03d", i)
		rows = append(rows, issueRow{ID: id, ProjectID: projectID, Payload: []byte("{}")})
		deliveries = append(deliveries, domain.AnalyticsDelivery{IssueID: id, Delivered: true})
	}
	if err := s.DB.CreateInBatches(rows, 100).Error; err != nil {
		t.Fatal(err)
	}
	// GORM 1.31.2 accepts separate field names; the AI claim that project_id is lost is false.
	var selected issueRow
	if err := s.DB.Select("id", "project_id").Where("id = ?", rows[0].ID).Take(&selected).Error; err != nil {
		t.Fatal(err)
	}
	if selected.ID != rows[0].ID || selected.ProjectID != "review-visible" {
		t.Fatalf("Select lost a column: %+v", selected)
	}
	// Repeated IDs across query batches must not cause repeated attribution of the whole slice.
	deliveries = append(deliveries, domain.AnalyticsDelivery{IssueID: rows[0].ID, Delivered: true}, domain.AnalyticsDelivery{IssueID: rows[1].ID}, domain.AnalyticsDelivery{IssueID: "review-missing", Delivered: true})
	groups := map[string]*domain.BusinessDomainUsage{"review-domain": {ID: "review-domain"}}
	if err := s.AnalyticsRepo.addBusinessDeliveryCounts(deliveries, map[string]string{"review-visible": "review-domain"}, groups); err != nil {
		t.Fatal(err)
	}
	if got := groups["review-domain"].Delivered; got != 501 {
		t.Fatalf("delivered=%d want 501 visible deliveries, excluding hidden/missing/undelivered", got)
	}
	if err := s.AnalyticsRepo.addBusinessDeliveryCounts(deliveries, map[string]string{}, map[string]*domain.BusinessDomainUsage{}); err != nil {
		t.Fatal(err)
	}
}

// 业务域报表把一次不带时间窗的 run 扫描同时喂给概览与交付指标。概览必须仍按
// 时间窗过滤（与自行取数的 Analytics 完全一致），不能因为预取范围变大而多算。
func TestAnalyticsWithPrefetchedRunsKeepsTimeWindow(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	prefix := fmt.Sprintf("analytics-prefetch-%d", time.Now().UnixNano())
	projectID := prefix + "-p"
	t.Cleanup(func() {
		for _, table := range []string{"run_events", "run_nodes", "run_artifacts", "runs", "issues"} {
			column := "id"
			if table == "run_events" || table == "run_nodes" || table == "run_artifacts" {
				column = "run_id"
			}
			if err := s.DB.Table(table).Where(column+" LIKE ?", prefix+"%").Delete(nil).Error; err != nil {
				t.Error(err)
			}
		}
	})
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	put := func(name string, start, end time.Time, status string) {
		t.Helper()
		issueID := prefix + "-issue-" + name
		if err := s.IssuesRepo.writeIssue(domain.Issue{ID: issueID, ProjectID: projectID, Status: "in_progress", Kind: "requirement", Source: "local"}); err != nil {
			t.Fatal(err)
		}
		runID := prefix + "-run-" + name
		run := domain.Run{ID: runID, WorkflowID: prefix, Status: status, CreatedAt: start, UpdatedAt: end,
			ExecutionContext: domain.TelemetryContext{ProjectID: projectID, IssueID: issueID},
			Usage:            domain.TokenUsage{TotalTokens: 100, InputTokens: 50, OutputTokens: 50},
			Nodes:            map[string]*domain.NodeRun{"work": {StartedAt: start, FinishedAt: end, Status: status}}}
		if err := s.RunsRepo.writeRun(run); err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Create(&[]runEventRow{
			{RunID: runID, Seq: 1, NodeID: "work", Type: "node.started", Ts: &start},
			{RunID: runID, Seq: 2, NodeID: "work", Type: "node.succeeded", Ts: &end},
			{RunID: runID, Seq: 3, Type: "run." + status, Ts: &end},
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	put("inside", from.Add(time.Hour), from.Add(2*time.Hour), "succeeded")
	put("edge", to.Add(-time.Hour), to.Add(-time.Minute), "succeeded")
	put("before", from.Add(-48*time.Hour), from.Add(-47*time.Hour), "succeeded")
	put("after", to.Add(time.Hour), to.Add(2*time.Hour), "failed")
	filter := domain.AnalyticsFilter{ProjectIDs: []string{projectID}, From: from, To: to, Grain: "day", TimeZone: "UTC"}
	windowed := s.AnalyticsRepo.Analytics(filter)
	runs, events, err := s.RunsRepo.RunSummariesWithEventsFor(domain.AnalyticsFilter{ProjectIDs: []string{projectID}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 {
		t.Fatalf("prefetch must keep out-of-window runs for delivery history: %d", len(runs))
	}
	if len(events[prefix+"-run-before"]) == 0 {
		t.Fatalf("prefetch must return timing events for history runs")
	}
	prefetched := s.AnalyticsRepo.analyticsWithRuns(filter, runs)
	if prefetched.Summary.TotalRuns != 2 || prefetched.Summary.TotalTokens != 200 {
		t.Fatalf("prefetched runs escaped the time window: %+v", prefetched.Summary)
	}
	if len(prefetched.Trend) != 1 || prefetched.Trend[0].Runs != 2 {
		t.Fatalf("prefetched trend escaped the time window: %+v", prefetched.Trend)
	}
	if windowed.Summary.TotalRuns != prefetched.Summary.TotalRuns || windowed.Summary.TotalTokens != prefetched.Summary.TotalTokens ||
		windowed.Summary.SuccessRate != prefetched.Summary.SuccessRate || windowed.Summary.UniqueIssues != prefetched.Summary.UniqueIssues ||
		len(windowed.Trend) != len(prefetched.Trend) || len(windowed.ProjectUsage) != len(prefetched.ProjectUsage) {
		t.Fatalf("prefetched aggregates differ from the windowed fetch: %+v vs %+v", prefetched.Summary, windowed.Summary)
	}
}

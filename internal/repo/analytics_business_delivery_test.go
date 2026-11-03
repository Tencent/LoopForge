package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestBusinessDeliveryMetrics(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			var s *Store
			var err error
			if driver == "mysql" {
				dsn := os.Getenv("AGENTFLOW_BUSINESS_DELIVERY_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("requires isolated local Docker MySQL")
				}
				s, err = Open(dsn)
			} else {
				s, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			checkBusinessDeliveryMetrics(t, s)
		})
	}
}

func checkBusinessDeliveryMetrics(t *testing.T, s *Store) {
	prefix := fmt.Sprintf("business-delivery-%d", time.Now().UnixNano())
	from := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	to := from.Add(24*time.Hour - time.Millisecond)
	projects := []domain.BusinessDomainProjectUsage{
		{AnalyticsProjectUsage: domain.AnalyticsProjectUsage{ProjectID: prefix + "-a"}, BusinessDomainID: prefix},
		{AnalyticsProjectUsage: domain.AnalyticsProjectUsage{ProjectID: prefix + "-b"}, BusinessDomainID: prefix},
	}
	t.Cleanup(func() {
		for _, table := range []string{"mr_delivery_states", "run_artifacts", "run_nodes", "run_events", "runs", "issues"} {
			column := "id"
			if table == "mr_delivery_states" {
				column = "mr_project"
			} else if table == "run_nodes" || table == "run_events" || table == "run_artifacts" {
				column = "run_id"
			}
			if err := s.DB.Table(table).Where(column+" LIKE ?", prefix+"%").Delete(nil).Error; err != nil {
				t.Error(err)
			}
		}
	})
	putIssue := func(id string, project int, status string, completed time.Time, ops ...domain.IssueOperation) domain.Issue {
		t.Helper()
		projectID := prefix + "-hidden"
		if project >= 0 {
			projectID = projects[project].ProjectID
		}
		issue := domain.Issue{ID: prefix + "-" + id, ProjectID: projectID, Status: status, Kind: "requirement", Source: "local", CompletedAt: completed, Operations: ops}
		// Persist historical evidence directly. PutIssue owns real-time lifecycle
		// timestamps and intentionally rejects client-provided completion times.
		if err := s.IssuesRepo.writeIssue(issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	putRun := func(id string, issue domain.Issue, status string, start, end time.Time, tokens int64, nested bool) {
		t.Helper()
		context := domain.TelemetryContext{IssueID: issue.ID} // legacy project fallback
		if nested {
			context.ParentRunID = prefix + "-parent"
		}
		if err := s.RunsRepo.writeRun(domain.Run{ID: prefix + "-run-" + id, WorkflowID: prefix, Status: status, CreatedAt: start, UpdatedAt: end, ExecutionContext: context, Usage: domain.TokenUsage{TotalTokens: tokens}, Nodes: map[string]*domain.NodeRun{"work": {StartedAt: start, FinishedAt: start.Add(10 * time.Minute), Status: status}}}); err != nil {
			t.Fatal(err)
		}
		finished := start.Add(10 * time.Minute)
		if err := s.DB.Create(&[]runEventRow{
			{RunID: prefix + "-run-" + id, Seq: 1, NodeID: "work", Type: "node.started", Ts: &start},
			{RunID: prefix + "-run-" + id, Seq: 2, NodeID: "work", Type: "node.succeeded", Ts: &finished},
			{RunID: prefix + "-run-" + id, Seq: 3, Type: "run." + status, Ts: &end},
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	a := putIssue("a", 0, "done", from.Add(2*time.Hour))
	putRun("a-failed", a, "failed", from.Add(-24*time.Hour), from.Add(-23*time.Hour), 10, false)
	putRun("a-success", a, "succeeded", from.Add(time.Hour), from.Add(2*time.Hour), 20, false)
	putRun("a-child", a, "succeeded", from.Add(time.Hour), from.Add(3*time.Hour), 5, true)
	b := putIssue("b", 0, "closed", time.Time{}, domain.IssueOperation{Action: "close", At: from.Add(6 * time.Hour)})
	putRun("b", b, "succeeded", from.Add(3*time.Hour), from.Add(5*time.Hour), 30, false)
	c := putIssue("c", 1, "done", time.Time{})
	putRun("c", c, "succeeded", from.Add(4*time.Hour), from.Add(14*time.Hour), 40, false)
	// The newest successful child must not substitute for a failed parent.
	failed := putIssue("failed", 0, "done", time.Time{})
	putRun("failed", failed, "failed", from.Add(time.Hour), from.Add(2*time.Hour), 1, false)
	putRun("failed-child", failed, "succeeded", from.Add(3*time.Hour), from.Add(4*time.Hour), 1, true)
	putIssue("missing", 0, "done", time.Time{})
	putIssue("open", 0, "in_progress", time.Time{})
	putIssue("backlog", 1, "todo", time.Time{})
	putIssue("cancelled", 0, "cancelled", time.Time{})
	outside := putIssue("outside", 0, "done", to.Add(time.Hour))
	putRun("outside", outside, "succeeded", from.Add(time.Hour), from.Add(2*time.Hour), 900, false)
	putIssue("hidden", -1, "done", from.Add(time.Hour))
	// A completion with no start is counted but excluded from duration samples.
	putIssue("no-start", 1, "completed", from.Add(16*time.Hour))
	putMR := func(iid int, issueID string, at *time.Time) {
		t.Helper()
		if err := s.DB.Create(&mrDeliveryStateRow{MRProject: prefix + "/repo", MRIID: iid, IssueID: issueID, State: "merged", MergedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	mergeAt := from.Add(17 * time.Hour) // next calendar day in Asia/Singapore
	putMR(1, a.ID, &mergeAt)
	putMR(2, a.ID, &mergeAt) // two MRs for the same requirement count twice
	putMR(3, b.ID, nil)
	putMR(4, prefix+"-hidden", &mergeAt)
	putMR(5, domain.BaselinePromoteIssueID, &mergeAt)
	putMR(6, a.ID, &to) // inclusive upper boundary
	if err := s.DB.Create(&runArtifactRow{ID: prefix + "-mr-artifact", RunID: prefix + "-run-a-success", Name: "mr-body", NodeID: "mr", Content: domain.GongfengMRURL(prefix+"/repo", 1)}).Error; err != nil {
		t.Fatal(err)
	}
	before := from.Add(-time.Millisecond)
	putMR(7, a.ID, &before)
	filter := domain.AnalyticsFilter{From: from, To: to, Grain: "day", TimeZone: "Asia/Singapore"}
	makeReport := func() domain.BusinessDomainReport {
		return domain.BusinessDomainReport{Projects: append([]domain.BusinessDomainProjectUsage(nil), projects...), BusinessDomains: []domain.BusinessDomainUsage{{ID: prefix}}}
	}
	out := makeReport()
	if err := s.populateBusinessDeliveryMetrics(&out, filter); err != nil {
		t.Fatal(err)
	}
	m := out.DeliveryMetrics
	if m.CompletionToMergeSamples != 1 || m.AverageCompletionToMergeMs != int64(15*time.Hour/time.Millisecond) {
		t.Fatalf("MR latency must use successful execution and real merge time: %+v", m)
	}
	if m.CompletedIssues != 4 || m.MergedMRs != 3 || m.MergedMRsMissingTime != 1 || m.CompletionTimeMissingIssues != 2 || m.EstimatedCompletionIssues != 1 {
		t.Fatalf("incorrect completion / merge clocks or scope: %+v", m)
	}
	if m.CycleSamples != 3 || m.AverageCycleDurationMs != int64(13*time.Hour/time.Millisecond) || m.P50CycleDurationMs != int64(10*time.Hour/time.Millisecond) || m.P95CycleDurationMs != int64(26*time.Hour/time.Millisecond) {
		t.Fatalf("must aggregate raw durations, including attempts before window: %+v", m)
	}
	if m.CompletedTokens != 105 || m.AverageTokensPerCompletedIssue != 26 {
		t.Fatalf("completion tokens must include earlier failures and children: %+v", m)
	}
	if m.ExecutionSamples != 3 || m.AverageExecutionDurationMs != int64(800*time.Second/time.Millisecond) {
		t.Fatalf("active time must include retries, union child overlap and exclude unknown samples: %+v", m)
	}
	if out.Projects[0].DeliveryMetrics.ExecutionSamples != 2 || out.Projects[0].DeliveryMetrics.AverageExecutionDurationMs != int64(15*time.Minute/time.Millisecond) {
		t.Fatalf("project active duration: %+v", out.Projects[0].DeliveryMetrics)
	}
	if out.BusinessDomains[0].DeliveryMetrics != m {
		// LatestCompletedAt pointers need value comparison.
		g := out.BusinessDomains[0].DeliveryMetrics
		if g.CompletedIssues != m.CompletedIssues || g.AverageCycleDurationMs != m.AverageCycleDurationMs || g.P95CycleDurationMs != m.P95CycleDurationMs {
			t.Fatalf("domain aggregation differs from total: %+v / %+v", g, m)
		}
	}
	if out.Projects[0].DeliveryMetrics.CompletedIssues != 2 || out.Projects[1].DeliveryMetrics.CompletedIssues != 2 {
		t.Fatalf("project attribution: %+v", out.Projects)
	}
	if len(out.DeliveryTrend) != 2 || out.DeliveryTrend[0].Date != "2026-10-20" || out.DeliveryTrend[0].CompletedIssues != 3 || out.DeliveryTrend[1].Date != "2026-10-21" || out.DeliveryTrend[1].MergedMRs != 3 {
		t.Fatalf("trend must use reporting timezone: %+v", out.DeliveryTrend)
	}
	empty := domain.BusinessDomainReport{}
	if err := s.populateBusinessDeliveryMetrics(&empty, filter); err != nil || empty.DeliveryMetrics.CompletedIssues != 0 || len(empty.DeliveryTrend) != 0 {
		t.Fatalf("empty authorized scope leaked data: %+v, %v", empty, err)
	}
	for grain, want := range map[string]string{"week": "2026-W43", "month": "2026-10"} {
		filter.Grain = grain
		out = makeReport()
		if err := s.populateBusinessDeliveryMetrics(&out, filter); err != nil || len(out.DeliveryTrend) != 1 || out.DeliveryTrend[0].Date != want {
			t.Fatalf("%s buckets: %+v, %v", grain, out.DeliveryTrend, err)
		}
	}
}

func TestBusinessExecutionDuration(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }
	run := func(id string, start, end int) domain.Run {
		return domain.Run{ID: id, CreatedAt: base, UpdatedAt: at(1440), Status: "succeeded", Nodes: map[string]*domain.NodeRun{"work": {StartedAt: at(start), FinishedAt: at(end)}}}
	}
	parent := run("parent", 0, 1440)
	parent.Nodes["work"].ChildRunID = "child"
	child := run("child", 10, 300)
	child.ExecutionContext.ParentRunID = parent.ID
	childEvents := []domain.RunEvent{
		{NodeID: "work", Type: "node.started", Timestamp: at(10)},
		{NodeID: "work", Type: "gate.waiting", Timestamp: at(12)},
		{NodeID: "work", Type: "node.started", Timestamp: at(250)},
		{NodeID: "work", Type: "node.succeeded", Timestamp: at(253)},
		{Type: "run.succeeded", Timestamp: at(300)},
	}
	spans := func(runs ...domain.Run) map[string][]domain.RunEvent {
		out := map[string][]domain.RunEvent{}
		for _, r := range runs {
			node := r.Nodes["work"]
			out[r.ID] = []domain.RunEvent{{NodeID: "work", Type: "node.started", Timestamp: node.StartedAt}, {NodeID: "work", Type: "node.succeeded", Timestamp: node.FinishedAt}, {Type: "run.succeeded", Timestamp: r.UpdatedAt}}
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		runs   []domain.Run
		events map[string][]domain.RunEvent
		want   int64
		known  bool
	}{
		{"parallel attempts", []domain.Run{run("a", 10, 30), run("b", 20, 40)}, spans(run("a", 10, 30), run("b", 20, 40)), 30, true},
		{"retries with a wait", []domain.Run{child}, map[string][]domain.RunEvent{"child": childEvents}, 5, true},
		{"nested SOP wait excluded", []domain.Run{parent, child}, map[string][]domain.RunEvent{"child": childEvents}, 5, true},
		{"clip to completion", []domain.Run{run("a", 1430, 1450)}, spans(run("a", 1430, 1450)), 10, true},
		{"observed zero", []domain.Run{run("zero", 10, 10)}, spans(run("zero", 10, 10)), 0, true},
		{"legacy node rows only", []domain.Run{run("legacy", 10, 1000)}, nil, 0, false},
		{"missing records", []domain.Run{{ID: "missing", CreatedAt: base}}, nil, 0, false},
		{"partially missing attempts", []domain.Run{run("a", 10, 20), {ID: "missing", CreatedAt: base}}, spans(run("a", 10, 20)), 0, false},
		{"missing child", []domain.Run{parent}, nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := businessExecutionDuration(businessIssueTiming{start: base, runs: tc.runs}, at(1440), tc.events)
			if (got != nil) != tc.known || (got != nil && *got != tc.want*60000) {
				t.Fatalf("execution=%v want %d minutes known=%v", got, tc.want, tc.known)
			}
		})
	}
}

func TestIssueCompletionTimestampLifecycle(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	issue := domain.Issue{ID: "completion-lifecycle", ProjectID: "platform", Status: "in_progress", CompletedAt: time.Now().Add(-time.Hour)}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	issue, _ = s.Issue(issue.ID)
	if !issue.CompletedAt.IsZero() {
		t.Fatal("open issue accepted an editable completion timestamp")
	}
	issue.Status = "done"
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	issue, _ = s.Issue(issue.ID)
	completedAt := issue.CompletedAt
	if completedAt.IsZero() {
		t.Fatal("completion transition was not recorded")
	}
	issue.Title = "edited after completion"
	issue.DeliveryUpdatedAt = completedAt.Add(time.Hour)
	issue.CompletedAt = completedAt.Add(time.Hour)
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	issue, _ = s.Issue(issue.ID)
	if !issue.CompletedAt.Equal(completedAt) {
		t.Fatal("ordinary edit or MR polling moved completion time")
	}
	if err := s.PutSyncedIssue(domain.Issue{ID: issue.ID, ProjectID: issue.ProjectID, Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	issue, _ = s.Issue(issue.ID)
	if !issue.CompletedAt.Equal(completedAt) {
		t.Fatal("tracker sync erased completion time")
	}
	issue.Status = "todo"
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	issue, _ = s.Issue(issue.ID)
	if !issue.CompletedAt.IsZero() {
		t.Fatal("reopening retained a terminal completion time")
	}
	issue.Status = "done"
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	issue, _ = s.Issue(issue.ID)
	if issue.CompletedAt.IsZero() || issue.CompletedAt.Before(completedAt) {
		t.Fatal("second completion was not recorded")
	}
	legacy := domain.Issue{ID: "legacy-completed", ProjectID: "platform", Status: "done"}
	if err := s.IssuesRepo.writeIssue(legacy); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(legacy); err != nil {
		t.Fatal(err)
	}
	legacy, _ = s.Issue(legacy.ID)
	if !legacy.CompletedAt.IsZero() {
		t.Fatal("editing legacy completed work must not manufacture a completion today")
	}
}

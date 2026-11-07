package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsMultiRepositoryMRCounts(t *testing.T) {
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
			checkAnalyticsMultiRepositoryMRCounts(t, s)
		})
	}
}

func checkAnalyticsMultiRepositoryMRCounts(t *testing.T, s *Store) {
	prefix := fmt.Sprintf("multi-mr-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, table := range []string{"run_artifacts", "run_nodes", "run_events", "runs", "issues", "projects", "mr_delivery_states"} {
			column := "id"
			if table == "mr_delivery_states" {
				column = "mr_project"
			} else if table == "run_artifacts" || table == "run_nodes" || table == "run_events" {
				column = "run_id"
			}
			s.DB.Table(table).Where(column+" LIKE ?", prefix+"%").Delete(nil)
		}
	})
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	project := domain.Project{ID: prefix, Name: "Multi repository"}
	if err := s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: prefix + "-issue", ProjectID: prefix, Status: "done", Kind: "requirement", CompletedAt: at.Add(30 * time.Minute)}
	// Historical completion evidence is persisted directly; PutIssue owns live timestamps.
	if err := s.IssuesRepo.writeIssue(issue); err != nil {
		t.Fatal(err)
	}
	a, b := prefix+"/frontend", prefix+"/backend"
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("%s-run-%d", prefix, i)
		run := domain.Run{ID: id, Status: "succeeded", CreatedAt: at, UpdatedAt: at.Add(10 * time.Minute),
			ExecutionContext: domain.TelemetryContext{IssueID: issue.ID, ProjectID: prefix},
			Repositories:     []domain.RepositoryContext{{GongfengProjectID: a}, {GongfengProjectID: b}},
			Artifacts: []domain.Artifact{
				{ID: id + "-a", Name: "mr-body", Content: domain.GongfengMRURL(a, 1)},
				{ID: id + "-b", Name: "mr-body", Content: domain.GongfengMRURL(b, 1)},
				{ID: id + "-patch", Name: "repository-patch", Content: domain.GongfengMRURL(prefix+"/unrelated", 99)},
			}, Usage: domain.TokenUsage{TotalTokens: 100}}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
		completed := at.Add(time.Duration(10+i*10) * time.Minute)
		if err := s.DB.Create(&runEventRow{RunID: id, Seq: 1, Type: "run.succeeded", Ts: &completed}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, repo := range []string{a, b} {
		if err := s.EnsureMRDeliveryState(repo, 1, domain.GongfengMRURL(repo, 1), issue.ID); err != nil {
			t.Fatal(err)
		}
		merged := at.Add(time.Hour)
		if repo == b {
			merged = at.Add(2 * time.Hour)
		}
		if err := s.UpdateMRDeliveryState(repo, 1, "merged", "master", merged, at.Add(3*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	filter := domain.AnalyticsFilter{ProjectID: prefix, From: at.Add(-time.Hour), To: at.Add(24 * time.Hour)}
	got := s.Analytics(filter)
	if len(got.Deliveries) != 1 || len(got.Deliveries[0].MRs) != 2 || got.Delivery.TotalMRs != 2 || got.Delivery.MRMerged != 2 || got.Delivery.WithMR != 1 || got.Delivery.Issues != 1 || got.Delivery.TotalTokens != 200 {
		t.Fatalf("one requirement with separate repository artifacts: summary=%+v deliveries=%+v", got.Delivery, got.Deliveries)
	}
	if got.Summary.MergedMRs != 2 {
		t.Fatalf("efficiency total must also count both MRs: %+v", got.Summary)
	}
	report, err := s.BusinessDomainReport(filter, []domain.Project{project}, nil)
	if err != nil || report.DeliveryMetrics.MergedMRs != 2 || report.DeliveryMetrics.CompletedIssues != 1 {
		t.Fatalf("business report must count two MRs and one requirement: %+v, %v", report.DeliveryMetrics, err)
	}
	if report.DeliveryMetrics.CompletionToMergeSamples != 2 || report.DeliveryMetrics.AverageCompletionToMergeMs != int64(70*time.Minute/time.Millisecond) {
		t.Fatalf("MR latency must have two samples (40m + 100m) and average 70m: %+v", report.DeliveryMetrics)
	}
	// Legacy work items can carry both links only on the issue, without artifacts.
	stored, _ := s.Issue(issue.ID)
	stored.SetDeliveryMRs([]domain.DeliveryMR{{Project: a, IID: 1, URL: domain.GongfengMRURL(a, 1)}, {Project: b, IID: 1, URL: domain.GongfengMRURL(b, 1)}})
	if err := s.IssuesRepo.writeIssue(stored); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Where("run_id LIKE ?", prefix+"%").Delete(&runArtifactRow{}).Error; err != nil {
		t.Fatal(err)
	}
	got = s.Analytics(filter)
	if got.Delivery.TotalMRs != 2 || got.Delivery.MRMerged != 2 || len(got.Deliveries[0].MRs) != 2 {
		t.Fatalf("MRs held only on the issue must not be reduced to the primary link: %+v", got.Delivery)
	}
	if err := s.UpdateMRDeliveryState(b, 1, "merged", domain.DefaultIntegrateBranch, at.Add(time.Hour), at.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = s.Analytics(filter)
	if got.Delivery.TotalMRs != 2 || got.Delivery.MRMerged != 1 || got.Delivery.MRIntegrated != 1 || got.Delivery.IssuesWithMergedMR != 1 {
		t.Fatalf("each MR retains its own target-branch state: %+v", got.Delivery)
	}
}

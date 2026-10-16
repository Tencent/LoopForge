package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"os"
	"testing"
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

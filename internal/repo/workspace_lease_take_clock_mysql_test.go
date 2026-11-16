package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Taking a lease stamps lease_renewed_at on the database clock too, not only
// renewing it. A holder whose clock runs two minutes behind the reader's has
// just taken the lease and not renewed yet: its lease_until looks two minutes
// old to the reader, but the lease must be kept until the database clock says
// it went unrenewed.
func TestMySQLLeaseJustTakenByAHolderWithASlowClockIsKept(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("local isolated MySQL required")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sqlDB, err := s.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	skew := func(d time.Duration) {
		t.Helper()
		if err := s.DB.Exec("SET timestamp = ?", float64(time.Now().Add(d).UnixMicro())/1e6).Error; err != nil {
			t.Fatal(err)
		}
	}
	defer s.DB.Exec("SET timestamp = DEFAULT")

	runID := fmt.Sprintf("lease-take-clock-%d", time.Now().UnixNano())
	if err := s.PutRun(domain.Run{ID: runID, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Where("run_id = ?", runID).Delete(&WorkspaceCheckpoint{})
	defer s.DB.Where("id = ?", runID).Delete(&runRow{})
	now := time.Now().UTC()
	token, err := s.BeginWorkspaceUse(runID, now.Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if released, err := s.ReleaseAbandonedWorkspaceUse(runID, token, now); err != nil || released {
		t.Fatalf("a lease its holder just took was released: %v %v", released, err)
	}
	skew(2 * time.Minute)
	if released, err := s.ReleaseAbandonedWorkspaceUse(runID, token, now); err != nil || !released {
		t.Fatalf("a lease nobody renewed for two minutes since it was taken was kept: %v %v", released, err)
	}
}

package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// lease_until is written on the holder's clock. Whether a holder on another
// replica stopped renewing is read from lease_renewed_at, which MySQL stamps
// with its own clock, so a holder whose clock runs behind keeps a lease it
// just renewed, and a lease nobody renewed for 45 seconds on the database
// clock is released. SET timestamp moves the database clock of the one
// connection the store is pinned to.
func TestMySQLAbandonedLeaseAgeIsMeasuredOnTheDatabaseClock(t *testing.T) {
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

	runID := fmt.Sprintf("lease-clock-%d", time.Now().UnixNano())
	if err := s.PutRun(domain.Run{ID: runID, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	defer s.DB.Where("run_id = ?", runID).Delete(&WorkspaceCheckpoint{})
	defer s.DB.Where("id = ?", runID).Delete(&runRow{})
	now := time.Now().UTC()
	token, err := s.BeginWorkspaceUse(runID, now)
	if err != nil {
		t.Fatal(err)
	}
	// The holder's clock runs two minutes behind the reader's and it renews
	// now: its lease_until looks two minutes old to the reader.
	if err := s.RenewWorkspaceUse(runID, token, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if released, err := s.ReleaseAbandonedWorkspaceUse(runID, token, now); err != nil || released {
		t.Fatalf("a lease its holder just renewed was released: %v %v", released, err)
	}
	// Two minutes later on the database clock nothing renewed it.
	skew(2 * time.Minute)
	if released, err := s.ReleaseAbandonedWorkspaceUse(runID, token, now); err != nil || !released {
		t.Fatalf("a lease nobody renewed for two minutes was kept: %v %v", released, err)
	}
	skew(0)

	// A lease taken before lease_renewed_at existed falls back to lease_until.
	legacy, err := s.BeginWorkspaceUse(runID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ?", runID).Updates(map[string]any{"lease_renewed_at": nil, "lease_until": now.Add(workspaceUseTerm - time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	if released, err := s.ReleaseAbandonedWorkspaceUse(runID, legacy, now); err != nil || !released {
		t.Fatalf("an unstamped lease silent for a minute was kept: %v %v", released, err)
	}
}

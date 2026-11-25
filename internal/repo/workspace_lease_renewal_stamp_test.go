package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

// Taking and renewing a lease stamp lease_renewed_at, and the abandoned-lease
// release goes by that stamp when it is there. A lease taken before the column
// existed has no stamp and is still judged by its lease_until.
func TestReleaseAbandonedWorkspaceUseJudgesByRenewalStampOrLegacyLeaseUntil(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for _, id := range []string{"stamped-take", "legacy-silent", "legacy-renewed", "stamp-decides"} {
		if err = s.PutRun(domain.Run{ID: id, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
			t.Fatal(err)
		}
	}
	take := func(runID string, at time.Time) string {
		t.Helper()
		token, err := s.BeginWorkspaceUse(runID, at)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	set := func(runID string, values map[string]any) {
		t.Helper()
		if err := s.DB.Model(&WorkspaceCheckpoint{}).Where("run_id = ?", runID).Updates(values).Error; err != nil {
			t.Fatal(err)
		}
	}
	release := func(runID, token string) bool {
		t.Helper()
		released, err := s.ReleaseAbandonedWorkspaceUse(runID, token, now)
		if err != nil {
			t.Fatal(err)
		}
		return released
	}

	// Taking the lease stamps it on the caller's clock (SQLite).
	taken := now.Add(-20 * time.Second)
	take("stamped-take", taken)
	var row WorkspaceCheckpoint
	if err := s.DB.Where("run_id = ?", "stamped-take").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.LeaseRenewedAt == nil || row.LeaseRenewedAt.Sub(taken).Abs() > time.Millisecond {
		t.Fatalf("taking a lease did not stamp lease_renewed_at with %v: %v", taken, row.LeaseRenewedAt)
	}

	// Unstamped lease whose lease_until says it was last renewed a minute ago.
	legacy := take("legacy-silent", now)
	set("legacy-silent", map[string]any{"lease_renewed_at": nil, "lease_until": now.Add(workspaceUseTerm - time.Minute)})
	if !release("legacy-silent", legacy) {
		t.Fatal("an unstamped lease silent for a minute was kept")
	}

	// Unstamped lease renewed ten seconds ago by its lease_until.
	renewed := take("legacy-renewed", now)
	set("legacy-renewed", map[string]any{"lease_renewed_at": nil, "lease_until": now.Add(workspaceUseTerm - 10*time.Second)})
	if release("legacy-renewed", renewed) {
		t.Fatal("an unstamped lease renewed ten seconds ago was released")
	}

	// A stamp from ten seconds ago keeps the lease even though lease_until,
	// written on the holder's clock, looks a minute old.
	stamped := take("stamp-decides", now)
	set("stamp-decides", map[string]any{"lease_renewed_at": now.Add(-10 * time.Second), "lease_until": now.Add(workspaceUseTerm - time.Minute)})
	if release("stamp-decides", stamped) {
		t.Fatal("a freshly stamped lease was released by its lease_until")
	}
}

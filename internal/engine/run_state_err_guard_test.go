package engine

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// The run-state helpers split into an error-swallowing pair (updateRun,
// updateRunAt) and an error-returning pair (updateRunErr, updateRunAtErr).
// These tests pin the contract each caller relies on.

func rejectRunWrites(t *testing.T, s *repo.Store, runID string) {
	t.Helper()
	for _, op := range []string{"INSERT", "UPDATE"} {
		stmt := "CREATE TRIGGER reject_run_" + strings.ToLower(op) + " BEFORE " + op + " ON runs " +
			"WHEN NEW.id = '" + runID + "' BEGIN SELECT RAISE(ABORT, 'run write rejected'); END"
		if err := s.DB.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpdateRunErrReportsStoreRejection(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := leaseEngine(t, s, "self")
	leaseRun(t, s, "run-store-reject", "waiting", "pending")
	rejectRunWrites(t, s, "run-store-reject")

	mutated := 0
	mutate := func(r *domain.Run) { mutated++; r.Nodes["node"].Activity = "should not persist" }

	if err := e.updateRunErr("run-store-reject", mutate); err == nil || !strings.Contains(err.Error(), "run write rejected") {
		t.Fatalf("updateRunErr err = %v, want the store rejection", err)
	}
	if updated, err := e.updateRunAtErr("run-store-reject", false, mutate); updated || err == nil {
		t.Fatalf("updateRunAtErr = (%t, %v), want (false, store error)", updated, err)
	}
	if e.updateRunAt("run-store-reject", false, mutate) {
		t.Fatal("updateRunAt reported success for a rejected write")
	}
	if e.updateRunSettling("run-store-reject", mutate) {
		t.Fatal("updateRunSettling reported success for a rejected write")
	}
	e.updateRun("run-store-reject", mutate) // must not panic; the error is only logged
	if mutated != 5 {
		t.Fatalf("mutate ran %d times, want 5 (once per attempted write)", mutated)
	}
	stored, ok := s.RunState("run-store-reject")
	if !ok {
		t.Fatal("run missing")
	}
	if got := stored.Nodes["node"].Activity; got != "" {
		t.Fatalf("rejected write leaked into the store: activity = %q", got)
	}
}

func TestUpdateRunHelpersRespectForeignLease(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := leaseEngine(t, s, "self")
	leaseRun(t, s, "run-foreign", "running", "running")
	if err := s.TryAcquireRunLease("run-foreign", "other", 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	before, ok := s.RunState("run-foreign")
	if !ok || before.OwnerInstanceID != "other" {
		t.Fatalf("run must be leased by another instance: %+v", before)
	}

	mutate := func(r *domain.Run) { r.Nodes["node"].Activity = "self wrote" }

	// A lease held elsewhere is a skipped update, not an error.
	if err := e.updateRunErr("run-foreign", mutate); err != nil {
		t.Fatalf("updateRunErr err = %v, want nil for a foreign lease", err)
	}
	if updated, err := e.updateRunAtErr("run-foreign", false, mutate); updated || err != nil {
		t.Fatalf("updateRunAtErr = (%t, %v), want (false, nil)", updated, err)
	}
	if e.updateRunAt("run-foreign", false, mutate) {
		t.Fatal("updateRunAt wrote through a foreign lease")
	}
	e.updateRun("run-foreign", mutate)
	if got, _ := s.RunState("run-foreign"); got.Nodes["node"].Activity != "" {
		t.Fatalf("lease-respecting helpers wrote a foreign run: activity = %q", got.Nodes["node"].Activity)
	}

	// Settling writes are allowed to land on an expired or foreign lease.
	if !e.updateRunSettling("run-foreign", mutate) {
		t.Fatal("updateRunSettling skipped a foreign-leased run")
	}
	if got, _ := s.RunState("run-foreign"); got.Nodes["node"].Activity != "self wrote" {
		t.Fatalf("settling write missing: activity = %q", got.Nodes["node"].Activity)
	}
}

func TestUpdateRunHelpersReportStoredWrite(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := leaseEngine(t, s, "self")
	leaseRun(t, s, "run-ok", "waiting", "pending")

	if err := e.updateRunErr("run-ok", func(r *domain.Run) { r.Nodes["node"].Activity = "a" }); err != nil {
		t.Fatalf("updateRunErr err = %v", err)
	}
	if !e.updateRunAt("run-ok", false, func(r *domain.Run) { r.Nodes["node"].Activity += "b" }) {
		t.Fatal("updateRunAt did not report a stored write")
	}
	e.updateRun("run-ok", func(r *domain.Run) { r.Nodes["node"].Activity += "c" })
	if got, _ := s.RunState("run-ok"); got.Nodes["node"].Activity != "abc" {
		t.Fatalf("activity = %q, want abc", got.Nodes["node"].Activity)
	}
	if e.updateRunAt("missing", false, func(*domain.Run) { t.Fatal("mutate ran for a missing run") }) {
		t.Fatal("updateRunAt reported a write for a missing run")
	}
	if err := e.updateRunErr("missing", func(*domain.Run) { t.Fatal("mutate ran for a missing run") }); err != nil {
		t.Fatalf("missing run is a skipped update, got err %v", err)
	}
}

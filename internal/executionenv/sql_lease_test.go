package executionenv

import (
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSQLLeaseStoreFencesRequestsAndGenerations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:lease_sql_test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&sqlLeaseRow{}, &sqlLeaseRequestRow{}); err != nil {
		t.Fatal(err)
	}
	store := NewSQLLeaseStore(db)
	first, err := store.Acquire(LeaseRequest{RequestID: "request-1", WorkerID: "worker-1", RequestLease: 60, Environment: Identity{EnvironmentID: "env-1", WorkspaceID: "ws-1", Generation: 1}})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.Acquire(LeaseRequest{RequestID: "request-1", WorkerID: "worker-1", RequestLease: 60, Environment: Identity{EnvironmentID: "env-1", WorkspaceID: "ws-1", Generation: 1}})
	if err != nil || retry.LeaseID != first.LeaseID {
		t.Fatalf("idempotent retry = %+v, %v", retry, err)
	}
	if _, err := store.Acquire(LeaseRequest{RequestID: "request-2", WorkerID: "worker-2", RequestLease: 60, Environment: Identity{EnvironmentID: "env-1", WorkspaceID: "ws-1", Generation: 2}}); err == nil {
		t.Fatal("active environment was reassigned")
	}
	if err := store.Release(first.LeaseID, "worker-1", 1); err != nil {
		t.Fatal(err)
	}
	next, err := store.Acquire(LeaseRequest{RequestID: "request-2", WorkerID: "worker-2", RequestLease: 60, Environment: Identity{EnvironmentID: "env-1", WorkspaceID: "ws-1", Generation: 2}})
	if err != nil || next.Identity.Generation != 2 {
		t.Fatalf("next generation = %+v, %v", next, err)
	}
}

func TestSQLLeaseStoreConcurrentAcquireHasOneWinner(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:lease_sql_race?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&sqlLeaseRow{}, &sqlLeaseRequestRow{}); err != nil {
		t.Fatal(err)
	}
	store := NewSQLLeaseStore(db)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for worker := 1; worker <= 2; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			_, err := store.Acquire(LeaseRequest{RequestID: "race-" + string(rune('0'+worker)), WorkerID: "worker-" + string(rune('0'+worker)), RequestLease: 30, Environment: Identity{EnvironmentID: "env-race", WorkspaceID: "ws-race", Generation: 1}})
			results <- err
		}(worker)
	}
	wg.Wait()
	close(results)
	var success int
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful concurrent allocations = %d", success)
	}
}

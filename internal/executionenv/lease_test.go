package executionenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLeaseStoreAcquireIsIdempotentAndGenerationFenced(t *testing.T) {
	store := NewLeaseStore()
	request := LeaseRequest{RequestID: "req-1", WorkerID: "worker-a", Environment: Identity{EnvironmentID: "env-1", WorkspaceID: "ws-1", Generation: 3}}
	first, err := store.Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.Acquire(request)
	if err != nil || retry.LeaseID != first.LeaseID {
		t.Fatalf("retry lease = %+v, err=%v", retry, err)
	}
	_, err = store.Acquire(LeaseRequest{RequestID: "req-2", WorkerID: "worker-b", Environment: request.Environment})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale worker acquire err=%v", err)
	}
	if err := store.Release(first.LeaseID, "worker-a", 2); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale generation release err=%v", err)
	}
	if err := store.Release(first.LeaseID, "worker-a", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Renew(first.LeaseID, "worker-a", 3, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew after release err=%v", err)
	}
}

func TestLeaseHandlerReturnsConflictForStaleGeneration(t *testing.T) {
	store := NewLeaseStore()
	h := Handler{Store: store}
	request := LeaseRequest{RequestID: "req-1", WorkerID: "worker-a", Environment: Identity{EnvironmentID: "env-1", WorkspaceID: "ws-1", Generation: 1}}
	body, _ := json.Marshal(request)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/environments/acquire", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("acquire status=%d body=%s", rec.Code, rec.Body.String())
	}
	body, _ = json.Marshal(LeaseRequest{RequestID: "req-2", WorkerID: "worker-b", Environment: request.Environment})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/environments/acquire", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale acquire status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLeaseHandlerRequiresBearerToken(t *testing.T) {
	h := Handler{Store: NewLeaseStore(), Token: "secret"}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/environments/acquire", bytes.NewReader([]byte(`{}`))))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without token status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/environments/acquire", bytes.NewReader([]byte(`{"requestId":"r","workerId":"w","identity":{"environmentId":"e","workspaceId":"w","generation":1}}`)))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with token status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLeaseStorePersistsAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/leases.json"
	store, err := OpenLeaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	req := LeaseRequest{RequestID: "persist-1", WorkerID: "worker", Environment: Identity{EnvironmentID: "env", WorkspaceID: "workspace", Generation: 4}}
	lease, err := store.Acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenLeaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Acquire(req)
	if err != nil || recovered.LeaseID != lease.LeaseID {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
}

func TestLeaseStoreFencesReusedRequestsAndOldGenerations(t *testing.T) {
	store := NewLeaseStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return now }
	req := LeaseRequest{RequestID: "one", WorkerID: "worker", Environment: Identity{EnvironmentID: "env", WorkspaceID: "workspace", Generation: 4}, RequestLease: 1}
	first, err := store.Acquire(req)
	if err != nil {
		t.Fatal(err)
	}
	forged := req
	forged.WorkerID = "other"
	if _, err := store.Acquire(forged); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("request conflict: %v", err)
	}
	now = now.Add(2 * time.Second)
	if _, err := store.Acquire(req); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired request retry: %v", err)
	}
	next := req
	next.RequestID, next.Environment.Generation = "two", 5
	second, err := store.Acquire(next)
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseID == second.LeaseID {
		t.Fatal("takeover reused expired lease")
	}
	old := req
	old.RequestID = "three"
	if _, err := store.Acquire(old); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old generation: %v", err)
	}
	if err := store.Release(first.LeaseID, req.WorkerID, req.Environment.Generation); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old release: %v", err)
	}
}

func TestLeaseStoreRollbackWhenPersistenceFails(t *testing.T) {
	store := NewLeaseStore()
	store.path = t.TempDir() // rename cannot replace a directory
	req := LeaseRequest{RequestID: "request", WorkerID: "worker", Environment: Identity{EnvironmentID: "env", WorkspaceID: "workspace", Generation: 1}}
	if _, err := store.Acquire(req); err == nil {
		t.Fatal("expected persistence error")
	}
	store.path = ""
	if _, err := store.Acquire(req); err != nil {
		t.Fatalf("failed write changed in-memory state: %v", err)
	}
}

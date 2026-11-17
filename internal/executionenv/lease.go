package executionenv

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrLeaseLost is returned when a stale worker attempts to use an execution
// environment after another generation has taken ownership of it.
var ErrLeaseLost = errors.New("execution environment lease lost")
var ErrRequestConflict = errors.New("execution environment request ID already used for another request")

// LeaseRequest is the durable control-plane part of an environment request.
// RequestID makes retries after a timeout safe: a repeated request returns the
// same lease instead of allocating another machine or workspace.
type LeaseRequest struct {
	RequestID    string   `json:"requestId"`
	Environment  Identity `json:"identity"`
	WorkerID     string   `json:"workerId"`
	RequestLease int      `json:"leaseSeconds,omitempty"`
}

// ReleaseRequest is the common wire shape for release calls and is exported
// so API/worker adapters do not need anonymous request structs.
type ReleaseRequest struct {
	LeaseID    string `json:"leaseId"`
	WorkerID   string `json:"workerId"`
	Generation int    `json:"generation"`
}

type Lease struct {
	LeaseID    string    `json:"leaseId"`
	RequestID  string    `json:"requestId"`
	Identity   Identity  `json:"identity"`
	WorkerID   string    `json:"workerId"`
	Workspace  string    `json:"workspace,omitempty"`
	Host       string    `json:"host,omitempty"`
	ExpiresAt  time.Time `json:"expiresAt"`
	ReleasedAt time.Time `json:"releasedAt,omitempty"`
}

// LeaseStore is intentionally small so a SQL-backed implementation can be
// introduced without changing the HTTP protocol. The in-memory implementation
// is useful for single-machine mode and protocol tests.
type LeaseStore struct {
	mu       sync.Mutex
	byKey    map[string]Lease
	byID     map[string]string
	byReq    map[string]string
	requests map[string]LeaseRequest
	clock    func() time.Time
	path     string
}

// LeaseBackend is the persistence boundary used by the HTTP manager. Both the
// single-process journal and the transactional SQL implementation implement
// the same protocol, so deployment can move storage without changing workers.
type LeaseBackend interface {
	Acquire(LeaseRequest) (Lease, error)
	Renew(string, string, int, time.Duration) (Lease, error)
	Release(string, string, int) error
}

type sqlLeaseRow struct {
	LeaseID       string    `gorm:"primaryKey;size:80"`
	RequestID     string    `gorm:"index;size:160;not null"`
	EnvironmentID string    `gorm:"uniqueIndex;size:255;not null"`
	WorkspaceID   string    `gorm:"size:255;not null"`
	Generation    int       `gorm:"not null"`
	WorkerID      string    `gorm:"size:255;not null"`
	Workspace     string    `gorm:"size:1024"`
	Host          string    `gorm:"size:255"`
	ExpiresAt     time.Time `gorm:"index;not null"`
	ReleasedAt    *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type sqlLeaseRequestRow struct {
	RequestID     string    `gorm:"primaryKey;size:160"`
	EnvironmentID string    `gorm:"size:255;not null"`
	WorkspaceID   string    `gorm:"size:255;not null"`
	Generation    int       `gorm:"not null"`
	WorkerID      string    `gorm:"size:255;not null"`
	LeaseID       string    `gorm:"size:80;not null"`
	CreatedAt     time.Time `gorm:"not null"`
}

func (sqlLeaseRequestRow) TableName() string { return "execution_environment_lease_requests" }

func (sqlLeaseRow) TableName() string { return "execution_environment_leases" }

// SQLLeaseStore uses row locks and unique request/environment keys. It is the
// store intended for an independently deployed manager; callers must run the
// supplied migration before serving traffic.
type SQLLeaseStore struct {
	db    *gorm.DB
	clock func() time.Time
}

func NewSQLLeaseStore(db *gorm.DB) *SQLLeaseStore {
	return &SQLLeaseStore{db: db, clock: time.Now}
}

func (s *SQLLeaseStore) Acquire(req LeaseRequest) (Lease, error) {
	if s == nil || s.db == nil {
		return Lease{}, errors.New("sql lease store is not configured")
	}
	if strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.Environment.EnvironmentID) == "" || strings.TrimSpace(req.Environment.WorkspaceID) == "" || strings.TrimSpace(req.WorkerID) == "" {
		return Lease{}, errors.New("requestId, environment, workspace and workerId are required")
	}
	if req.Environment.Generation < 0 || req.RequestLease < 0 || req.RequestLease > 3600 {
		return Lease{}, errors.New("invalid generation or lease duration")
	}
	ttl := time.Duration(req.RequestLease) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := s.clock().UTC()
	var out Lease
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var request sqlLeaseRequestRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", req.RequestID).First(&request).Error; err == nil {
			if request.WorkerID != req.WorkerID || request.EnvironmentID != req.Environment.EnvironmentID || request.WorkspaceID != req.Environment.WorkspaceID || request.Generation != req.Environment.Generation {
				return ErrRequestConflict
			}
			var prior sqlLeaseRow
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("lease_id = ?", request.LeaseID).First(&prior).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && prior.ReleasedAt == nil && prior.ExpiresAt.After(now) {
				out = sqlLeaseToLease(prior)
				return nil
			}
			return ErrLeaseLost
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var current sqlLeaseRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("environment_id = ?", req.Environment.EnvironmentID).First(&current).Error
		if err == nil {
			if req.Environment.Generation < current.Generation {
				return ErrLeaseLost
			}
			if current.ReleasedAt == nil && current.ExpiresAt.After(now) {
				if current.WorkerID != req.WorkerID || current.WorkspaceID != req.Environment.WorkspaceID || current.Generation != req.Environment.Generation {
					return ErrLeaseLost
				}
				current.ExpiresAt = now.Add(ttl)
				current.RequestID = req.RequestID
				current.UpdatedAt = now
				if err := tx.Save(&current).Error; err != nil {
					return err
				}
				if err := tx.Create(&sqlLeaseRequestRow{RequestID: req.RequestID, EnvironmentID: req.Environment.EnvironmentID, WorkspaceID: req.Environment.WorkspaceID, Generation: req.Environment.Generation, WorkerID: req.WorkerID, LeaseID: current.LeaseID, CreatedAt: now}).Error; err != nil {
					return err
				}
				out = sqlLeaseToLease(current)
				return nil
			}
			oldLeaseID := current.LeaseID
			newLeaseID := randomLeaseID()
			current.RequestID = req.RequestID
			current.LeaseID = newLeaseID
			current.WorkspaceID = req.Environment.WorkspaceID
			current.Generation = req.Environment.Generation
			current.WorkerID = req.WorkerID
			current.ExpiresAt = now.Add(ttl)
			current.ReleasedAt = nil
			current.UpdatedAt = now
			if err := tx.Model(&sqlLeaseRow{}).Where("lease_id = ?", oldLeaseID).Updates(map[string]any{
				"lease_id": newLeaseID, "request_id": current.RequestID, "workspace_id": current.WorkspaceID,
				"generation": current.Generation, "worker_id": current.WorkerID, "expires_at": current.ExpiresAt,
				"released_at": nil, "updated_at": current.UpdatedAt,
			}).Error; err != nil {
				return err
			}
			if err := tx.Create(&sqlLeaseRequestRow{RequestID: req.RequestID, EnvironmentID: req.Environment.EnvironmentID, WorkspaceID: req.Environment.WorkspaceID, Generation: req.Environment.Generation, WorkerID: req.WorkerID, LeaseID: current.LeaseID, CreatedAt: now}).Error; err != nil {
				return err
			}
			out = sqlLeaseToLease(current)
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		row := sqlLeaseRow{LeaseID: randomLeaseID(), RequestID: req.RequestID, EnvironmentID: req.Environment.EnvironmentID, WorkspaceID: req.Environment.WorkspaceID, Generation: req.Environment.Generation, WorkerID: req.WorkerID, ExpiresAt: now.Add(ttl), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := tx.Create(&sqlLeaseRequestRow{RequestID: req.RequestID, EnvironmentID: req.Environment.EnvironmentID, WorkspaceID: req.Environment.WorkspaceID, Generation: req.Environment.Generation, WorkerID: req.WorkerID, LeaseID: row.LeaseID, CreatedAt: now}).Error; err != nil {
			return err
		}
		out = sqlLeaseToLease(row)
		return nil
	})
	return out, err
}

func (s *SQLLeaseStore) Renew(leaseID, workerID string, generation int, ttl time.Duration) (Lease, error) {
	if s == nil || s.db == nil {
		return Lease{}, errors.New("sql lease store is not configured")
	}
	if ttl > time.Hour {
		return Lease{}, errors.New("invalid lease duration")
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := s.clock().UTC()
	var out Lease
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row sqlLeaseRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("lease_id = ?", leaseID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLeaseLost
			}
			return err
		}
		if row.WorkerID != workerID || row.Generation != generation || row.ReleasedAt != nil || !row.ExpiresAt.After(now) {
			return ErrLeaseLost
		}
		row.ExpiresAt = now.Add(ttl)
		row.UpdatedAt = now
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		out = sqlLeaseToLease(row)
		return nil
	})
	return out, err
}

func (s *SQLLeaseStore) Release(leaseID, workerID string, generation int) error {
	if s == nil || s.db == nil {
		return errors.New("sql lease store is not configured")
	}
	now := s.clock().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row sqlLeaseRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("lease_id = ?", leaseID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLeaseLost
			}
			return err
		}
		if row.WorkerID != workerID || row.Generation != generation {
			return ErrLeaseLost
		}
		if row.ReleasedAt != nil {
			return nil
		}
		if !row.ExpiresAt.After(now) {
			return ErrLeaseLost
		}
		row.ReleasedAt = &now
		row.UpdatedAt = now
		return tx.Save(&row).Error
	})
}

func sqlLeaseToLease(row sqlLeaseRow) Lease {
	return Lease{LeaseID: row.LeaseID, RequestID: row.RequestID, Identity: Identity{EnvironmentID: row.EnvironmentID, WorkspaceID: row.WorkspaceID, Generation: row.Generation}, WorkerID: row.WorkerID, Workspace: row.Workspace, Host: row.Host, ExpiresAt: row.ExpiresAt, ReleasedAt: valueTime(row.ReleasedAt)}
}

func valueTime(v *time.Time) time.Time {
	if v == nil {
		return time.Time{}
	}
	return *v
}

func NewLeaseStore() *LeaseStore {
	return &LeaseStore{byKey: map[string]Lease{}, byID: map[string]string{}, byReq: map[string]string{}, requests: map[string]LeaseRequest{}, clock: time.Now}
}

type leaseJournal struct {
	Leases        []Lease                 `json:"leases"`
	Requests      map[string]LeaseRequest `json:"requests"`
	RequestLeases map[string]string       `json:"requestLeases"`
}

// OpenLeaseStore keeps lease ownership across a manager restart. The file is
// only a small recovery journal; Docker/workspace inspection remains the
// execution host's responsibility. A failed read is returned so callers do
// not silently start a second manager with an empty lease set.
func OpenLeaseStore(path string) (*LeaseStore, error) {
	store := NewLeaseStore()
	store.path = strings.TrimSpace(path)
	if store.path == "" {
		return store, nil
	}
	raw, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	var journal leaseJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		// Compatibility with the initial experimental array format.
		if legacyErr := json.Unmarshal(raw, &journal.Leases); legacyErr != nil {
			return nil, fmt.Errorf("decode lease store: %w", err)
		}
	}
	for _, lease := range journal.Leases {
		if lease.LeaseID == "" {
			continue
		}
		key := leaseKey(lease.Identity)
		store.byKey[key], store.byID[lease.LeaseID] = lease, key
		if lease.RequestID != "" {
			store.byReq[lease.RequestID] = lease.LeaseID
			store.requests[lease.RequestID] = LeaseRequest{RequestID: lease.RequestID, WorkerID: lease.WorkerID, Environment: lease.Identity}
		}
	}
	for id, req := range journal.Requests {
		store.requests[id] = req
	}
	for id, leaseID := range journal.RequestLeases {
		store.byReq[id] = leaseID
		if req, ok := store.requests[id]; ok {
			store.byID[leaseID] = leaseKey(req.Environment)
		}
	}
	return store, nil
}

func (s *LeaseStore) persistLocked() error {
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	leases := make([]Lease, 0, len(s.byKey))
	for _, lease := range s.byKey {
		leases = append(leases, lease)
	}
	raw, err := json.MarshalIndent(leaseJournal{Leases: leases, Requests: s.requests, RequestLeases: s.byReq}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func leaseKey(identity Identity) string {
	return strings.TrimSpace(identity.EnvironmentID)
}

func randomLeaseID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("lease-%d", time.Now().UnixNano())
	}
	return "lease-" + hex.EncodeToString(b)
}

func (s *LeaseStore) Acquire(req LeaseRequest) (Lease, error) {
	if s == nil {
		return Lease{}, errors.New("lease store is not configured")
	}
	if strings.TrimSpace(req.RequestID) == "" || strings.TrimSpace(req.Environment.EnvironmentID) == "" || strings.TrimSpace(req.Environment.WorkspaceID) == "" || strings.TrimSpace(req.WorkerID) == "" {
		return Lease{}, errors.New("requestId, environment, workspace and workerId are required")
	}
	if req.Environment.Generation < 0 || req.RequestLease < 0 || req.RequestLease > 3600 {
		return Lease{}, errors.New("invalid generation or lease duration")
	}
	ttl := time.Duration(req.RequestLease) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := s.clock().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.requests[req.RequestID]; ok {
		if prior.WorkerID != req.WorkerID || prior.Environment != req.Environment {
			return Lease{}, ErrRequestConflict
		}
		id := s.byReq[req.RequestID]
		key := s.byID[id]
		lease := s.byKey[key]
		if lease.LeaseID == id && lease.ReleasedAt.IsZero() && lease.ExpiresAt.After(now) {
			return lease, nil
		}
		return Lease{}, ErrLeaseLost
	}
	key := leaseKey(req.Environment)
	current, exists := s.byKey[key]
	if exists && req.Environment.Generation < current.Identity.Generation {
		return Lease{}, ErrLeaseLost
	}
	if exists && current.ReleasedAt.IsZero() && current.ExpiresAt.After(now) {
		if current.WorkerID != req.WorkerID || current.Identity != req.Environment {
			return Lease{}, ErrLeaseLost
		}
		previous := current
		current.ExpiresAt = now.Add(ttl)
		s.byKey[key] = current
		s.requests[req.RequestID], s.byReq[req.RequestID] = req, current.LeaseID
		if err := s.persistLocked(); err != nil {
			s.byKey[key] = previous
			delete(s.requests, req.RequestID)
			delete(s.byReq, req.RequestID)
			return Lease{}, err
		}
		return current, nil
	}
	lease := Lease{LeaseID: randomLeaseID(), RequestID: req.RequestID, Identity: req.Environment, WorkerID: req.WorkerID, ExpiresAt: now.Add(ttl)}
	s.byKey[key], s.byID[lease.LeaseID], s.byReq[req.RequestID] = lease, key, lease.LeaseID
	s.requests[req.RequestID] = req
	if err := s.persistLocked(); err != nil {
		if exists {
			s.byKey[key] = current
		} else {
			delete(s.byKey, key)
		}
		delete(s.byID, lease.LeaseID)
		delete(s.byReq, req.RequestID)
		delete(s.requests, req.RequestID)
		return Lease{}, err
	}
	return lease, nil
}

func (s *LeaseStore) Renew(leaseID, workerID string, generation int, ttl time.Duration) (Lease, error) {
	if ttl > time.Hour {
		return Lease{}, errors.New("invalid lease duration")
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.byID[leaseID]
	lease, ok := s.byKey[key]
	if !ok || lease.LeaseID != leaseID || lease.ReleasedAt.After(time.Time{}) || lease.WorkerID != workerID || lease.Identity.Generation != generation || !lease.ExpiresAt.After(s.clock().UTC()) {
		return Lease{}, ErrLeaseLost
	}
	previous := lease
	lease.ExpiresAt = s.clock().UTC().Add(ttl)
	s.byKey[key] = lease
	if err := s.persistLocked(); err != nil {
		s.byKey[key] = previous
		return Lease{}, err
	}
	return lease, nil
}

func (s *LeaseStore) Release(leaseID, workerID string, generation int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.byID[leaseID]
	lease, ok := s.byKey[key]
	if !ok || lease.LeaseID != leaseID || lease.WorkerID != workerID || lease.Identity.Generation != generation {
		return ErrLeaseLost
	}
	previous := lease
	if lease.ReleasedAt.IsZero() {
		if !lease.ExpiresAt.After(s.clock().UTC()) {
			return ErrLeaseLost
		}
		lease.ReleasedAt = s.clock().UTC()
		s.byKey[key] = lease
	}
	if err := s.persistLocked(); err != nil {
		s.byKey[key] = previous
		return err
	}
	return nil
}

// Handler exposes the lease protocol for an independently deployed sandbox
// manager. Token is optional for loopback/single-machine mode; when set every
// request must carry the matching Bearer token.
type Handler struct {
	Store LeaseBackend
	Token string
}

func decodeJSON(r *http.Request, out any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(out)
}
func encodeJSON(w http.ResponseWriter, value any) error { return json.NewEncoder(w).Encode(value) }
func leaseStatus(err error) int {
	if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrRequestConflict) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	store := h.Store
	if store == nil {
		http.Error(w, "lease store is not configured", http.StatusServiceUnavailable)
		return
	}
	token := strings.TrimSpace(h.Token)
	if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	var out any
	var status = http.StatusOK
	switch r.URL.Path {
	case "/v1/environments/acquire":
		if r.Method != http.MethodPost {
			status = http.StatusMethodNotAllowed
			break
		}
		var req LeaseRequest
		if err := decodeJSON(r, &req); err != nil {
			status = http.StatusBadRequest
			out = map[string]string{"error": err.Error()}
			break
		}
		lease, err := store.Acquire(req)
		if err != nil {
			status = leaseStatus(err)
			out = map[string]string{"error": err.Error()}
			break
		}
		out = lease
	case "/v1/environments/renew":
		if r.Method != http.MethodPost {
			status = http.StatusMethodNotAllowed
			break
		}
		var req struct {
			LeaseID, WorkerID string
			Generation        int
			LeaseSeconds      int `json:"leaseSeconds"`
		}
		if err := decodeJSON(r, &req); err != nil {
			status = http.StatusBadRequest
			out = map[string]string{"error": err.Error()}
			break
		}
		lease, err := store.Renew(req.LeaseID, req.WorkerID, req.Generation, time.Duration(req.LeaseSeconds)*time.Second)
		if err != nil {
			status = leaseStatus(err)
			out = map[string]string{"error": err.Error()}
			break
		}
		out = lease
	case "/v1/environments/release":
		if r.Method != http.MethodPost {
			status = http.StatusMethodNotAllowed
			break
		}
		var req struct {
			LeaseID, WorkerID string
			Generation        int
		}
		if err := decodeJSON(r, &req); err != nil {
			status = http.StatusBadRequest
			out = map[string]string{"error": err.Error()}
			break
		}
		if err := store.Release(req.LeaseID, req.WorkerID, req.Generation); err != nil {
			status = leaseStatus(err)
			out = map[string]string{"error": err.Error()}
		} else {
			out = map[string]bool{"released": true}
		}
	default:
		status = http.StatusNotFound
	}
	w.WriteHeader(status)
	if out != nil {
		_ = encodeJSON(w, out)
	}
}

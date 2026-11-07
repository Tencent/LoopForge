package tracker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agentflow/internal/domain"
)

type fakeMRDeliveryStore struct {
	discoverErr error
	repos       []domain.Repository
	ensured     []struct {
		project string
		iid     int
		issueID string
	}
	pending []domain.MRDeliveryState
	updates []struct {
		project      string
		iid          int
		state        string
		targetBranch string
		mergedAt     time.Time
		checkedAt    time.Time
	}
}

func (f *fakeMRDeliveryStore) DiscoverMRDeliveryStatesFromArtifacts() error {
	return f.discoverErr
}

func (f *fakeMRDeliveryStore) MRDeliveryStatesNeedingRefresh(_ time.Time) ([]domain.MRDeliveryState, error) {
	return f.pending, nil
}

func (f *fakeMRDeliveryStore) UpdateMRDeliveryState(project string, iid int, state string, targetBranch string, mergedAt, checkedAt time.Time) error {
	f.updates = append(f.updates, struct {
		project      string
		iid          int
		state        string
		targetBranch string
		mergedAt     time.Time
		checkedAt    time.Time
	}{project, iid, state, targetBranch, mergedAt, checkedAt})
	return nil
}

func (f *fakeMRDeliveryStore) ReconcileMergedMRDeliveries() error  { return nil }
func (f *fakeMRDeliveryStore) ReconcileCompletedDeliveries() error { return nil }
func (f *fakeMRDeliveryStore) ReconcileUnlinkedDeliveryMRs() error { return nil }
func (f *fakeMRDeliveryStore) ReconcileDeliveryMRStates() error    { return nil }
func (f *fakeMRDeliveryStore) Repositories() []domain.Repository   { return f.repos }
func (f *fakeMRDeliveryStore) MergedWorkMRs(string) ([]domain.MRDeliveryState, error) {
	return nil, nil
}

func (f *fakeMRDeliveryStore) ApplyDefaultBranchLanding(string, string, bool) error {
	return nil
}

func (f *fakeMRDeliveryStore) EnsureMRDeliveryState(project string, iid int, _ string, issueID string) error {
	f.ensured = append(f.ensured, struct {
		project string
		iid     int
		issueID string
	}{project, iid, issueID})
	return nil
}

func TestSyncMRDeliveryStatesUpdatesPendingRows(t *testing.T) {
	store := &fakeMRDeliveryStore{pending: []domain.MRDeliveryState{{
		MRProject: "example/team-project", MRIID: 212, State: "unknown",
	}}}
	svc := NewService()
	svc.gongfeng = NewMCPClient("http://invalid", nil)
	mergedAt := time.Date(2026, 10, 12, 3, 8, 59, 0, time.UTC)
	svc.mrCache.Store("example/team-project#212", mrCacheEntry{state: "merged", mergedAt: mergedAt, at: time.Now()})

	svc.syncMRDeliveryStates(store)
	if len(store.updates) != 1 {
		t.Fatalf("updates=%d want 1", len(store.updates))
	}
	if store.updates[0].state != "merged" || store.updates[0].iid != 212 {
		t.Fatalf("update=%+v", store.updates[0])
	}
	if !store.updates[0].mergedAt.Equal(mergedAt) || !store.updates[0].checkedAt.After(mergedAt) {
		t.Fatalf("merge time must survive the cache and remain distinct from poll time: %+v", store.updates[0])
	}
}

func TestSyncMRDeliveryStatesKeepsMergedRowsOnLookupFailure(t *testing.T) {
	store := &fakeMRDeliveryStore{pending: []domain.MRDeliveryState{{MRProject: "org/repo", MRIID: 1, State: "merged"}}}
	svc := NewService()
	svc.gongfeng = NewMCPClient("http://invalid", nil)
	svc.mrCache.Store("org/repo#1", mrCacheEntry{state: "unknown", at: time.Now()})
	svc.syncMRDeliveryStates(store)
	if len(store.updates) != 0 {
		t.Fatalf("failed lookup overwrote a merged row: %+v", store.updates)
	}
}

func TestDiscoverPromoteMRsRegistersBaselineToDefault(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05"}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			state, _ := req.Params.Arguments["state"].(string)
			payload := `{"data":[]}`
			if state == "opened" {
				payload = `{"data":[{"iid":284,"state":"opened","source_branch":"agentflow/integration","target_branch":"master"}]}`
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":%q}]}}`, req.ID, payload)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	t.Cleanup(gateway.Close)

	store := &fakeMRDeliveryStore{repos: []domain.Repository{{
		GongfengProjectID: "example/team-project",
		DefaultBranch:     "master",
		IntegrateBranch:   "agentflow/integration",
	}}}
	svc := &Service{gongfeng: NewMCPClient(gateway.URL, nil)}
	svc.discoverPromoteMRs(store)
	if len(store.ensured) != 1 || store.ensured[0].iid != 284 || store.ensured[0].issueID != domain.BaselinePromoteIssueID {
		t.Fatalf("ensured=%+v", store.ensured)
	}
	if len(store.updates) != 1 || store.updates[0].state != "opened" || store.updates[0].targetBranch != "master" {
		t.Fatalf("updates=%+v", store.updates)
	}
}

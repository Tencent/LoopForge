package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestClaimedTaskRenewsWhileWaitingForWorkspace(t *testing.T) {
	renewed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if filepath.Base(r.URL.Path) == "renew" {
			select {
			case renewed <- struct{}{}:
			default:
			}
			_ = json.NewEncoder(w).Encode(RenewResponse{LeaseUntil: time.Now().Add(time.Minute)})
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "local-test", StatePath: filepath.Join(t.TempDir(), "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	gate := d.workspaceGate("preparing")
	gate.Lock()
	var once sync.Once
	unlock := func() { once.Do(gate.Unlock) }
	defer unlock()
	done := make(chan struct{})
	go func() {
		d.executeTask(context.Background(), domain.RunnerTask{ID: "preparing-task", RunID: "preparing", Action: "unsupported-test-action"})
		close(done)
	}()
	select {
	case <-renewed:
	case <-time.After(2 * time.Second):
		t.Fatal("claimed task did not renew before acquiring its workspace")
	}
	unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not release its lease loop")
	}
}

func TestTaskLeaseLoopRenewsDuringPreparationAndCancelsOnRejection(t *testing.T) {
	for _, kind := range []string{"cancel", "lost", "unauthorized", "transient"} {
		t.Run(kind, func(t *testing.T) {
			var renewals atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := renewals.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if n == 1 && kind == "transient" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if n < 3 {
					_ = json.NewEncoder(w).Encode(RenewResponse{LeaseUntil: time.Now().Add(time.Minute)})
					return
				}
				switch kind {
				case "lost":
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"error":"task lease lost"}`))
				case "unauthorized":
					w.WriteHeader(http.StatusUnauthorized)
				default:
					_ = json.NewEncoder(w).Encode(RenewResponse{DesiredState: domain.RunnerTaskDesiredCancel})
				}
			}))
			defer server.Close()
			d := &Daemon{client: NewClient(server.URL)}
			until := time.Now().Add(time.Minute)
			ctx, stop := d.keepTaskLease(context.Background(), domain.RunnerTask{ID: "slow-preparation", LeaseUntil: &until}, 10*time.Millisecond)
			defer stop()
			if !taskLeaseManaged(ctx) {
				t.Fatal("processes would start a duplicate renewal loop")
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("task preparation did not stop after cancellation or lost lease")
			}
			if renewals.Load() < 3 {
				t.Fatalf("preparation did not renew continuously: %d", renewals.Load())
			}
		})
	}
}

func TestTaskLeaseStopsAtLastAcknowledgedExpiry(t *testing.T) {
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-finished:
		}
	}))
	defer server.Close()
	defer close(finished)
	d := &Daemon{client: NewClient(server.URL)}
	until := time.Now().Add(40 * time.Millisecond)
	ctx, stop := d.keepTaskLease(context.Background(), domain.RunnerTask{ID: "lost-network", LeaseUntil: &until}, time.Second)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("preparation outlived its acknowledged lease after losing network")
	}
}

package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func renewLeaseTestDaemon(t *testing.T, handler http.HandlerFunc) *Daemon {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Daemon{client: NewClient(server.URL)}
}

func TestRenewTaskLeaseOutcomes(t *testing.T) {
	acknowledged := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	future := time.Now().Add(time.Minute)
	past := time.Now().Add(-time.Second)
	cases := []struct {
		name      string
		until     *time.Time
		handler   http.HandlerFunc
		wantUntil *time.Time
		wantEnd   bool
	}{
		{
			name:  "acknowledged expiry replaces previous",
			until: &future,
			handler: func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(RenewResponse{LeaseUntil: acknowledged})
			},
			wantUntil: &acknowledged,
		},
		{
			name:  "missing expiry keeps previous",
			until: &future,
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{}`))
			},
			wantUntil: &future,
		},
		{
			name:  "missing expiry without previous stays unbounded",
			until: nil,
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{}`))
			},
			wantUntil: nil,
		},
		{
			name:  "server requests cancellation",
			until: &future,
			handler: func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(RenewResponse{LeaseUntil: acknowledged, DesiredState: strings.ToUpper(domain.RunnerTaskDesiredCancel)})
			},
			wantUntil: &future,
			wantEnd:   true,
		},
		{
			name:  "lease lost",
			until: &future,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"task lease lost"}`))
			},
			wantUntil: &future,
			wantEnd:   true,
		},
		{
			name:  "runner session rejected",
			until: &future,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantUntil: &future,
			wantEnd:   true,
		},
		{
			name:  "transient failure before expiry keeps renewing",
			until: &future,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantUntil: &future,
		},
		{
			name:  "transient failure without known expiry keeps renewing",
			until: nil,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantUntil: nil,
		},
		{
			name:  "transient failure after expiry stops",
			until: &past,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantUntil: &past,
			wantEnd:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := make(chan string, 1)
			d := renewLeaseTestDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				tc.handler(w, r)
			})
			gotUntil, gotEnd := d.renewTaskLease(context.Background(), "task-1", tc.until)
			// An already expired lease bounds the request deadline in the past,
			// so the request fails before reaching the server.
			if expired := tc.until != nil && !tc.until.After(time.Now()); !expired {
				select {
				case path := <-paths:
					if path != "/runners/tasks/task-1/lease/renew" {
						t.Fatalf("renewed path = %q", path)
					}
				default:
					t.Fatal("renewal did not reach the server")
				}
			}
			if gotEnd != tc.wantEnd {
				t.Fatalf("end = %v, want %v", gotEnd, tc.wantEnd)
			}
			switch {
			case tc.wantUntil == nil && gotUntil != nil:
				t.Fatalf("until = %v, want nil", *gotUntil)
			case tc.wantUntil != nil && (gotUntil == nil || !gotUntil.Equal(*tc.wantUntil)):
				t.Fatalf("until = %v, want %v", gotUntil, *tc.wantUntil)
			}
		})
	}
}

func TestRenewTaskLeaseIgnoresErrorsAfterShutdown(t *testing.T) {
	d := renewLeaseTestDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	past := time.Now().Add(-time.Second)
	gotUntil, gotEnd := d.renewTaskLease(ctx, "task-1", &past)
	if gotEnd {
		t.Fatal("a renewal error during shutdown must not be reported as a lease failure")
	}
	if gotUntil == nil || !gotUntil.Equal(past) {
		t.Fatalf("until = %v, want unchanged %v", gotUntil, past)
	}
}

func TestRenewTaskLeaseBoundsRequestByKnownExpiry(t *testing.T) {
	finished := make(chan struct{})
	defer close(finished)
	d := renewLeaseTestDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-finished:
		}
	})
	until := time.Now().Add(50 * time.Millisecond)
	start := time.Now()
	_, gotEnd := d.renewTaskLease(context.Background(), "task-1", &until)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("renewal was not bounded by the acknowledged expiry: %v", elapsed)
	}
	if !gotEnd {
		t.Fatal("a renewal that failed past the acknowledged expiry must stop the task")
	}
}

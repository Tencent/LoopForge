package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

type renewLeaseRoundTripFunc func(*http.Request) (*http.Response, error)

func (f renewLeaseRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRenewTaskLeaseCapsRequestDeadline pins the request deadline itself: at
// most 20 seconds from now, and never past the last acknowledged expiry.
// Hanging-server tests cannot observe the 20 second cap without waiting for it.
func TestRenewTaskLeaseCapsRequestDeadline(t *testing.T) {
	soon := time.Now().Add(5 * time.Second)
	later := time.Now().Add(time.Hour)
	cases := []struct {
		name  string
		until *time.Time
		want  func(start time.Time) time.Time
	}{
		{name: "unknown expiry uses 20 seconds", until: nil, want: func(start time.Time) time.Time { return start.Add(20 * time.Second) }},
		{name: "distant expiry uses 20 seconds", until: &later, want: func(start time.Time) time.Time { return start.Add(20 * time.Second) }},
		{name: "near expiry bounds the request", until: &soon, want: func(time.Time) time.Time { return soon }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var deadline time.Time
			var hasDeadline bool
			d := &Daemon{client: NewClient("http://lease.invalid")}
			d.client.http = &http.Client{Transport: renewLeaseRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				deadline, hasDeadline = r.Context().Deadline()
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{}`)),
					Request:    r,
				}, nil
			})}
			start := time.Now()
			if _, end := d.renewTaskLease(context.Background(), "task-1", tc.until); end {
				t.Fatal("a successful renewal must not stop the task")
			}
			if !hasDeadline {
				t.Fatal("renewal request has no deadline")
			}
			want := tc.want(start)
			if diff := deadline.Sub(want); diff < -time.Second || diff > time.Second {
				t.Fatalf("request deadline = %v, want about %v (diff %v)", deadline, want, diff)
			}
		})
	}
}

// TestRenewTaskLeaseAbortsInFlightRequestOnShutdown guards that the renewal
// request is derived from the lease context: stopping the task must not wait
// for a hanging server up to the 20 second cap.
func TestRenewTaskLeaseAbortsInFlightRequestOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	defer close(finished)
	d := renewLeaseTestDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		select {
		case <-r.Context().Done():
		case <-finished:
		}
	})
	start := time.Now()
	until, end := d.renewTaskLease(ctx, "task-1", nil)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("in-flight renewal ignored shutdown for %v", elapsed)
	}
	if end || until != nil {
		t.Fatalf("shutdown renewal = (%v, %v), want (nil, false)", until, end)
	}
}

// TestTaskLeaseLoopCarriesAcknowledgedExpiry guards the loop's use of the
// expiry returned by renewTaskLease: a server-extended lease must keep the
// task alive past the expiry it was claimed with.
func TestTaskLeaseLoopCarriesAcknowledgedExpiry(t *testing.T) {
	var renewals atomic.Int32
	d := renewLeaseTestDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		renewals.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(RenewResponse{LeaseUntil: time.Now().Add(time.Hour)})
	})
	claimed := time.Now().Add(80 * time.Millisecond)
	ctx, stop := d.keepTaskLease(context.Background(), domain.RunnerTask{ID: "extended", LeaseUntil: &claimed}, 20*time.Millisecond)
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatalf("task stopped at its claimed expiry although the server extended the lease (renewals=%d)", renewals.Load())
	case <-time.After(300 * time.Millisecond):
	}
	if got := renewals.Load(); got < 2 {
		t.Fatalf("renewals = %d, want the loop to keep renewing", got)
	}
}

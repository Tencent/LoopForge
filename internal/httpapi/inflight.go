package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"agentflow/internal/schedule"
)

// inflightTracker counts long-lived work that runs inside the API process
// (assistant generations, clarifications, group follow-ups, SOP handoffs) so a
// replaced process can finish it before exiting.
type inflightTracker struct {
	mu      sync.Mutex
	n       int
	changed chan struct{}
}

func newInflightTracker() *inflightTracker {
	return &inflightTracker{changed: make(chan struct{})}
}

// inflightTasks is process-wide: generations outlive the request that
// started them and are not owned by any single handler instance.
var inflightTasks = newInflightTracker()

// Add registers one task and returns its idempotent completion func.
func (t *inflightTracker) Add() func() {
	t.mu.Lock()
	t.n++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.n--
			close(t.changed)
			t.changed = make(chan struct{})
			t.mu.Unlock()
		})
	}
}

func (t *inflightTracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n
}

// Wait blocks until no task is registered or ctx ends.
func (t *inflightTracker) Wait(ctx context.Context) error {
	for {
		t.mu.Lock()
		n, changed := t.n, t.changed
		t.mu.Unlock()
		if n <= 0 {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// backgroundLoop separates claiming new work from the work already claimed:
// stopping the loop must not cancel a running model turn.
// Background controls the API process's autonomous work during shutdown or
// handover: StopClaiming → WaitInflight → Stop.
type Background struct {
	recoveryGate   <-chan struct{}
	scheduler      *schedule.Service
	runnerControls *runnerControlHub
	handedOver     atomic.Bool
	stopOnce       sync.Once
	claimOnce      sync.Once
}

// BackgroundOptions tunes startup of a process that may be replacing another.
type BackgroundOptions struct {
	// RecoveryGate, when set, postpones startup recovery of interrupted work
	// (clarifications, schedule jobs) until it closes, so a successor does not
	// reclaim work its still-draining predecessor is finishing.
	RecoveryGate <-chan struct{}
}

// StopClaiming stops polling for new follow-ups, SOP handoffs and schedule
// ticks without cancelling work that is already running.
func (b *Background) StopClaiming() {
	if b == nil {
		return
	}
	b.claimOnce.Do(func() {
		if b.scheduler != nil {
			b.scheduler.Stop()
		}
	})
}

// HandedOver records that another process now owns the public listener:
// runner sockets are dropped so runners reconnect there, and this process
// reaches runners through it for the work it is still finishing.
func (b *Background) HandedOver() {
	if b == nil {
		return
	}
	b.handedOver.Store(true)
	if b.runnerControls != nil {
		b.runnerControls.handOver()
	}
}

func (b *Background) handedOverToSuccessor() bool {
	return b != nil && b.handedOver.Load()
}

// WaitInflight waits until every tracked in-process task has finished.
func (b *Background) WaitInflight(ctx context.Context) error {
	return inflightTasks.Wait(ctx)
}

// Inflight reports the number of tracked in-process tasks.
func (b *Background) Inflight() int {
	return inflightTasks.Count()
}

// Stop cancels whatever background work is still running and joins it.
func (b *Background) Stop() {
	if b == nil {
		return
	}
	b.stopOnce.Do(func() {
		b.StopClaiming()
	})
}

// runAfterGate runs fn now when gate is nil, otherwise once gate closes.
func runAfterGate(gate <-chan struct{}, fn func()) {
	if gate == nil {
		fn()
		return
	}
	go func() {
		<-gate
		fn()
	}()
}

// AgentToolHandler exposes only the native agent-tool MCP endpoints of h. It
// backs each process's private loopback listener, so a CLI keeps reaching the
// process that holds its in-memory tool session across a handover.
func AgentToolHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/internal/agent-tools/") {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/executionenv"
	"agentflow/internal/runtime"
)

// environmentLease is attached to a runner task while its local sandbox is
// open. It prevents a delayed cleanup or a reconnected daemon from deleting a
// newer generation's workspace.
type environmentLease struct {
	client *executionenv.Client
	lease  executionenv.Lease
	worker string
	stop   context.CancelFunc
	done   chan struct{}
	once   sync.Once
	mu     sync.RWMutex
	err    error
}

type environmentLeaseContextKey struct{}

func withEnvironmentLease(ctx context.Context, lease *environmentLease) context.Context {
	return context.WithValue(ctx, environmentLeaseContextKey{}, lease)
}

func environmentLeaseFromContext(ctx context.Context) *environmentLease {
	lease, _ := ctx.Value(environmentLeaseContextKey{}).(*environmentLease)
	return lease
}

func (d *Daemon) acquireEnvironmentLease(ctx context.Context, task domain.RunnerTask) (*environmentLease, error) {
	base := d.cfg.EnvironmentManagerURL
	if base == "" {
		return nil, nil
	}
	if task.RunID == "" || task.Workspace == "" {
		return nil, fmt.Errorf("execution environment lease requires run and workspace identity")
	}
	requestID := task.ID
	if requestID == "" {
		sum := sha256.Sum256([]byte(task.RunID + task.Workspace))
		requestID = "task-" + hex.EncodeToString(sum[:8])
	}
	client := executionenv.NewClient(base, d.cfg.EnvironmentManagerToken)
	lease, err := client.Acquire(ctx, executionenv.LeaseRequest{RequestID: requestID, WorkerID: d.state.RunnerID, RequestLease: 3600, Environment: executionenv.Identity{EnvironmentID: task.RunID, WorkspaceID: task.Workspace, Generation: task.RunGeneration}})
	if err != nil {
		return nil, err
	}
	managedCtx, stop := context.WithCancel(context.Background())
	managed := &environmentLease{client: client, lease: lease, worker: d.state.RunnerID, stop: stop, done: make(chan struct{})}
	go managed.renewLoop(managedCtx)
	return managed, nil
}

func (l *environmentLease) release() {
	if l == nil || l.client == nil {
		return
	}
	l.mu.Lock()
	stop := l.stop
	l.stop = nil
	lease := l.lease
	l.mu.Unlock()
	if stop != nil {
		stop()
	}
	l.once.Do(func() { close(l.done) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = l.client.Release(ctx, lease.LeaseID, l.worker, lease.Identity.Generation)
}

func (l *environmentLease) renewLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if err := l.renew(renewCtx, time.Hour); err != nil {
				cancel()
				l.mu.Lock()
				l.err = err
				l.mu.Unlock()
				l.once.Do(func() { close(l.done) })
				return
			}
			cancel()
		}
	}
}

func (l *environmentLease) check() error {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	err := l.err
	l.mu.RUnlock()
	return err
}

// leaseSandbox fences every command with the environment lease. A lease
// expiry cancels the command even when the runner process itself is healthy.
type leaseSandbox struct {
	runtime.Sandbox
	lease *environmentLease
}

func (s *leaseSandbox) Exec(ctx context.Context, command runtime.CommandSpec, sink runtime.EventSink) (string, error) {
	if err := s.lease.check(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.lease.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	out, err := s.Sandbox.Exec(ctx, command, sink)
	if leaseErr := s.lease.check(); leaseErr != nil {
		return out, leaseErr
	}
	return out, err
}

func (l *environmentLease) renew(ctx context.Context, ttl time.Duration) error {
	if l == nil || l.client == nil {
		return nil
	}
	l.mu.RLock()
	current := l.lease
	l.mu.RUnlock()
	lease, err := l.client.Renew(ctx, current.LeaseID, l.worker, current.Identity.Generation, ttl)
	if err != nil {
		return fmt.Errorf("renew execution environment lease: %w", err)
	}
	l.mu.Lock()
	l.lease = lease
	l.mu.Unlock()
	return nil
}

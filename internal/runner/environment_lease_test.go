package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/runtime"
)

func TestIndependentEnvironmentManagerFailsClosed(t *testing.T) {
	_, err := New(Config{ServerURL: "http://localhost", BootstrapToken: "bootstrap", EnvironmentManagerURL: "http://localhost:8091", EnvironmentManagerToken: "token"})
	if err == nil || !strings.Contains(err.Error(), "lease fencing is incomplete") {
		t.Fatalf("unsafe independent manager configuration accepted: %v", err)
	}
}

type blockingLeaseSandbox struct{ started chan struct{} }

func (*blockingLeaseSandbox) ID() string                  { return "sandbox" }
func (*blockingLeaseSandbox) Workspace() string           { return "/workspace" }
func (*blockingLeaseSandbox) Close(context.Context) error { return nil }
func (s *blockingLeaseSandbox) Exec(ctx context.Context, _ runtime.CommandSpec, _ runtime.EventSink) (string, error) {
	close(s.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestLeaseSandboxCancelsExecutionWhenLeaseIsLost(t *testing.T) {
	lease := &environmentLease{done: make(chan struct{})}
	sb := &leaseSandbox{Sandbox: &blockingLeaseSandbox{started: make(chan struct{})}, lease: lease}
	result := make(chan error, 1)
	go func() {
		_, err := sb.Exec(context.Background(), runtime.CommandSpec{Argv: []string{"true"}}, nil)
		result <- err
	}()
	select {
	case <-sb.Sandbox.(*blockingLeaseSandbox).started:
	case <-time.After(time.Second):
		t.Fatal("sandbox did not start")
	}
	lease.mu.Lock()
	lease.err = errors.New("lease expired")
	lease.mu.Unlock()
	close(lease.done)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "lease expired") && !errors.Is(err, context.Canceled) {
			t.Fatalf("execution error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("execution continued after lease loss")
	}
}

func TestEnvironmentLeaseUsesShortRenewalWindow(t *testing.T) {
	if environmentLeaseTTL != 90*time.Second || environmentLeaseRenewal != 20*time.Second {
		t.Fatalf("lease timing changed: ttl=%s renewal=%s", environmentLeaseTTL, environmentLeaseRenewal)
	}
}

package runtime

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestWindowsTerminalRejectsWithoutAcquiringLease(t *testing.T) {
	var pool TerminalPool
	released := false
	_, err := pool.HandleSandbox(context.Background(), nil, "run:user", domain.WorkspaceTerminalRequest{Operation: "open"}, func() { released = true })
	if err == nil || !strings.Contains(err.Error(), "Windows Runner") {
		t.Fatalf("unsupported terminal was not reported: %v", err)
	}
	if released || pool.HasScope("run:user") || pool.HasRunScope("run") {
		t.Fatal("rejected terminal took ownership of a lease")
	}
}

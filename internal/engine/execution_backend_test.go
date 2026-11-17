package engine

import (
	"context"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/executionenv"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

type recordingExecutionBackend struct {
	requests  []executionenv.AcquireRequest
	destroyed []string
}

func (b *recordingExecutionBackend) Acquire(_ context.Context, req executionenv.AcquireRequest) (runtime.Sandbox, error) {
	b.requests = append(b.requests, req)
	return &recordingSandbox{}, nil
}
func (*recordingExecutionBackend) WorkspaceExists(string) (bool, error) { return true, nil }
func (b *recordingExecutionBackend) Destroy(id string) error {
	b.destroyed = append(b.destroyed, id)
	return nil
}

func TestExecutionBackendReceivesChildWorkspaceIdentity(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	e := leaseEngine(t, store, "worker")
	e.leaseTTL = time.Minute
	backend := &recordingExecutionBackend{}
	e.executionBackend = backend
	parent := domain.Run{ID: "parent", Status: "running", Nodes: map[string]*domain.NodeRun{}}
	if err := store.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	child := domain.Run{ID: "child", Status: "running", ExecutionContext: domain.TelemetryContext{WorkspaceRunID: parent.ID}, Nodes: map[string]*domain.NodeRun{}}
	if err := store.PutRun(child); err != nil {
		t.Fatal(err)
	}
	owned, err := e.acquireLease(child)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.acquireRunEnvironment(context.Background(), owned, domain.SandboxProfile{ID: "local", Driver: "local"}, runtime.SandboxRequirements{}); err != nil {
		t.Fatal(err)
	}
	if len(backend.requests) != 1 {
		t.Fatalf("acquire calls = %d", len(backend.requests))
	}
	identity := backend.requests[0].Identity
	if identity.EnvironmentID != child.ID || identity.WorkspaceID != parent.ID || identity.Generation != owned.Generation {
		t.Fatalf("incorrect execution identity: %+v", identity)
	}
	if err := e.releaseRunEnvironment(child.ID, true); err != nil {
		t.Fatal(err)
	}
	if len(backend.destroyed) != 1 || backend.destroyed[0] != child.ID {
		t.Fatalf("destroyed = %v", backend.destroyed)
	}
}

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/opensandbox"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// TestDeleteRunKeepsRunWhenOpenSandboxReleaseFails covers the 3819f1c9 review:
// a provider DELETE failure must keep the run (its ExecutionResourceID is the
// only handle on the live sandbox) and surface a retryable error, and a later
// delete must release the sandbox and remove the run.
func TestDeleteRunKeepsRunWhenOpenSandboxReleaseFails(t *testing.T) {
	const scope, runID, sandboxID = "del-test", "run-os-delete", "sbx-del-1"
	sum := sha256.Sum256([]byte(scope + "/" + runID))
	binding := hex.EncodeToString(sum[:24])
	var deletes, failDeletes atomic.Int32
	failDeletes.Store(1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sandboxes/"+sandboxID {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": sandboxID, "metadata": map[string]string{"loopforge-binding": binding}})
		case http.MethodDelete:
			deletes.Add(1)
			if failDeletes.Add(-1) >= 0 {
				http.Error(w, "provider down", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(provider.Close)

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	client, err := opensandbox.New(opensandbox.Config{
		Endpoint: provider.URL, APIKey: "opensandbox-test", RunnerURL: "https://loopforge.example.test",
		RunnerImage: "runner:test", Scope: scope, RunnerSigningKey: strings.Repeat("testing", 5),
	}, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	eng := New(store, runtime.NewManagerWithLimit(t.TempDir(), 1))
	if err := eng.ConfigureOpenSandbox(DefaultOpenSandboxBackend, client, []string{"default"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "wf-os-delete", Name: "wf", ProjectID: "default", SandboxProfileID: "test-native"}
	run := domain.Run{
		ID: runID, Status: "failed", WorkflowID: wf.ID, WorkflowSnapshot: wf,
		ExecutionContext: domain.TelemetryContext{
			ProjectID: "default", ExecutionPool: domain.ExecutionPoolOpenSandbox,
			ExecutionResourceID: sandboxID, ExecutionRunnerID: domain.OpenSandboxRunnerID(runID),
		},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}

	err = eng.DeleteRun(runID)
	if !errors.Is(err, ErrExecutionResourceRelease) {
		t.Fatalf("DeleteRun with a failing provider = %v, want ErrExecutionResourceRelease", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "重试") {
		t.Fatalf("error %q should carry the provider failure and a retry hint", err)
	}
	kept, ok := store.RunState(runID)
	if !ok {
		t.Fatal("run was deleted although its sandbox was not released")
	}
	if kept.ExecutionContext.ExecutionResourceID != sandboxID {
		t.Fatalf("sandbox handle lost after failed release: %+v", kept.ExecutionContext)
	}

	if err := eng.DeleteRun(runID); err != nil {
		t.Fatalf("retried DeleteRun after the provider recovered = %v", err)
	}
	if _, ok := store.RunState(runID); ok {
		t.Fatal("retried delete did not remove the run")
	}
	if got := deletes.Load(); got != 2 {
		t.Fatalf("provider DELETE calls = %d, want 2 (failed + retried)", got)
	}
}

// A provider that answers a create with a definitive 4xx created nothing. The
// create intent must be cleared, so a retry provisions a sandbox instead of
// reading the pending flag as an unknown outcome forever.
func TestOpenSandboxCreateRejectionClearsPendingIntent(t *testing.T) {
	const scope, runID = "reject-test", "run-os-reject"
	var items atomic.Int32
	var creates atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sandboxes" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "pagination": map[string]any{"hasNextPage": false, "totalItems": items.Load()}})
		case r.URL.Path == "/v1/sandboxes" && r.Method == http.MethodPost:
			if creates.Add(1) == 1 {
				http.Error(w, "invalid image", http.StatusBadRequest)
				return
			}
			items.Store(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sbx-reject-ok", "metadata": map[string]string{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	client, err := opensandbox.New(opensandbox.Config{
		Endpoint: provider.URL, APIKey: "opensandbox-test", RunnerURL: "https://loopforge.example.test",
		RunnerImage: "runner:test", Scope: scope, RunnerSigningKey: strings.Repeat("testing", 5),
	}, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	eng := New(store, runtime.NewManagerWithLimit(t.TempDir(), 1))
	if err := eng.ConfigureOpenSandbox(DefaultOpenSandboxBackend, client, []string{"reject-project"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "wf-os-reject", Name: "wf", ProjectID: "reject-project", SandboxProfileID: "test-native"}
	run := domain.Run{
		ID: runID, Status: "running", WorkflowID: wf.ID, WorkflowSnapshot: wf, OwnerInstanceID: eng.instanceID,
		Generation: 1, LeaseUntil: time.Now().UTC().Add(time.Minute),
		ExecutionContext: domain.TelemetryContext{
			ProjectID: "reject-project", ExecutionPool: domain.ExecutionPoolOpenSandbox,
			ExecutionProviderID: DefaultOpenSandboxBackend, ExecutionRunnerID: domain.OpenSandboxRunnerID(runID),
		},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.RunState(runID)
	if _, err := eng.provisionOpenSandbox(context.Background(), client, owner, "n1"); err == nil {
		t.Fatal("a definitive create rejection must fail the attempt")
	}
	after, _ := store.RunState(runID)
	if after.ExecutionContext.ExecutionResourcePending {
		t.Fatal("a rejected create left the pending flag set; the run can never provision again")
	}
	if after.ExecutionContext.ExecutionResourceID != "" {
		t.Fatalf("a rejected create bound a sandbox: %+v", after.ExecutionContext)
	}
	owner, _ = store.RunState(runID)
	id, err := eng.provisionOpenSandbox(context.Background(), client, owner, "n1")
	if err != nil || id != "sbx-reject-ok" {
		t.Fatalf("retry after a rejected create = %q, %v; want the created sandbox", id, err)
	}
	if creates.Load() != 2 {
		t.Fatalf("provider create calls = %d, want 2 (rejected + retried)", creates.Load())
	}
}

// A run whose runner registration is already gone must still delete once the
// sandbox is released, instead of failing the whole delete with a 503.
func TestDeleteRunWithoutRunnerRegistrationReleasesSandbox(t *testing.T) {
	const scope, runID, sandboxID = "no-runner", "run-os-norunner", "sbx-norunner"
	sum := sha256.Sum256([]byte(scope + "/" + runID))
	binding := hex.EncodeToString(sum[:24])
	var deletes atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sandboxes/"+sandboxID {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": sandboxID, "metadata": map[string]string{"loopforge-binding": binding}})
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(provider.Close)
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	client, err := opensandbox.New(opensandbox.Config{
		Endpoint: provider.URL, APIKey: "opensandbox-test", RunnerURL: "https://loopforge.example.test",
		RunnerImage: "runner:test", Scope: scope, RunnerSigningKey: strings.Repeat("testing", 5),
	}, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	eng := New(store, runtime.NewManagerWithLimit(t.TempDir(), 1))
	if err := eng.ConfigureOpenSandbox(DefaultOpenSandboxBackend, client, []string{"norunner"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "wf-os-norunner", Name: "wf", ProjectID: "norunner", SandboxProfileID: "test-native"}
	run := domain.Run{
		ID: runID, Status: "failed", WorkflowID: wf.ID, WorkflowSnapshot: wf,
		ExecutionContext: domain.TelemetryContext{
			ProjectID: "norunner", ExecutionPool: domain.ExecutionPoolOpenSandbox,
			ExecutionResourceID: sandboxID,
		},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := eng.DeleteRun(runID); err != nil {
		t.Fatalf("DeleteRun with no runner registration = %v, want success", err)
	}
	if _, ok := store.RunState(runID); ok {
		t.Fatal("run was kept although the sandbox was released")
	}
	if deletes.Load() != 1 {
		t.Fatalf("provider DELETE calls = %d, want 1", deletes.Load())
	}
}

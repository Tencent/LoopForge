package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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

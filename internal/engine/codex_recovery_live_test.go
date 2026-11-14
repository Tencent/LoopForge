package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Runs the real engine and installed Codex CLI against a local HTTP endpoint.
func TestRealCodexEngineWaitsOnCredentials(t *testing.T) {
	if os.Getenv("LOOPFORGE_REAL_CODEX_TEST") != "1" {
		t.Skip("set LOOPFORGE_REAL_CODEX_TEST=1 to run the installed Codex CLI")
	}
	_, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	var updatedCredentialRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") == "Bearer updated-local-test-key" {
			updatedCredentialRequests.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid_api_key","type":"authentication_error"}}`)
	}))
	defer srv.Close()
	t.Setenv("NO_PROXY", "localhost,127.0.0.1")
	t.Setenv("no_proxy", "localhost,127.0.0.1")
	e, store := newRecoveryEngine(t)
	if err := store.PutUser(domain.User{ID: 3, LoginName: "codex-probe"}); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/v1"
	if _, err := store.UpdateUserCredentials(3, repo.UserCredentialUpdate{CodexAPIKey: "local-test-key", CodexBaseURL: &base}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAgent(domain.AgentDefinition{ID: "codex-probe", Name: "Codex probe", ExecutorID: "codex", Model: "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "codex-live", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: "codex-probe", MaxAttempts: 1}}}
	run := domain.Run{ID: "codex-real-auth", WorkflowID: wf.ID, Status: "running", WorkflowSnapshot: wf, ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 3}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "pending"}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 1 {
		t.Fatalf("resumed: %v", resumed)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.Run(run.ID)
		if n := got.Nodes["n"]; n != nil && n.Status == "waiting_approval" && got.Status == "waiting" {
			if n.WaitReason != "model_credentials" || n.ModelErrorClass != "auth" || n.Attempt != 1 || n.ExecutionSequence != 1 || requests.Load() == 0 {
				t.Fatalf("requests=%d node=%+v", requests.Load(), n)
			}
			t.Logf("real Codex requests=%d status=%s waitReason=%s class=%s attempt=%d", requests.Load(), n.Status, n.WaitReason, n.ModelErrorClass, n.Attempt)
			if _, err := store.UpdateUserCredentials(3, repo.UserCredentialUpdate{CodexAPIKey: "updated-local-test-key"}); err != nil {
				t.Fatal(err)
			}
			if err := e.Retry(run.ID, "n", "", ""); err != nil {
				t.Fatal(err)
			}
			for time.Now().Before(deadline) {
				retried, _ := store.Run(run.ID)
				if node := retried.Nodes["n"]; node != nil && node.Status == "waiting_approval" && node.ExecutionSequence == 2 && retried.Status == "waiting" {
					if updatedCredentialRequests.Load() == 0 || node.Attempt != 1 {
						t.Fatalf("updated credential requests=%d node=%+v", updatedCredentialRequests.Load(), node)
					}
					t.Logf("manual retry used updated credential: HTTP requests=%d attempt=%d execution=%d", updatedCredentialRequests.Load(), node.Attempt, node.ExecutionSequence)
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatal("manual credential retry did not reach the second gate")
		}
		if got.Status == "failed" {
			t.Fatalf("run failed: %+v", got.Nodes["n"])
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := store.Run(run.ID)
	t.Fatalf("timed out: run=%s node=%+v requests=%d", got.Status, got.Nodes["n"], requests.Load())
}

func TestRealCodexEngineRateLimitFallback(t *testing.T) {
	if os.Getenv("LOOPFORGE_REAL_CODEX_TEST") != "1" {
		t.Skip("set LOOPFORGE_REAL_CODEX_TEST=1 to run the installed Codex CLI")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal(err)
	}
	var primary, backup atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request JSON: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch body.Model {
		case "primary":
			primary.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate_limit_error","type":"rate_limit_error"}}`)
		case "backup":
			backup.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"invalid_api_key","type":"authentication_error"}}`)
		default:
			t.Errorf("unexpected model: %s", body.Model)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	t.Setenv("NO_PROXY", "localhost,127.0.0.1")
	t.Setenv("no_proxy", "localhost,127.0.0.1")
	e, store := newRecoveryEngine(t)
	if err := store.PutUser(domain.User{ID: 3, LoginName: "codex-fallback"}); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/v1"
	if _, err := store.UpdateUserCredentials(3, repo.UserCredentialUpdate{CodexAPIKey: "local-test-key", CodexBaseURL: &base}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAgent(domain.AgentDefinition{ID: "fallback-probe", Name: "Fallback probe", ExecutorID: "codex", Model: "primary", FallbackModels: []string{"backup"}}); err != nil {
		t.Fatal(err)
	}
	initial, maximum := 1, 1
	wf := domain.WorkflowDefinition{ID: "codex-fallback", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: "fallback-probe", MaxAttempts: 1, ModelRetry: &domain.ModelRetryPolicy{InitialSeconds: &initial, MaxSeconds: &maximum}}}}
	run := domain.Run{ID: "codex-real-fallback", WorkflowID: wf.ID, Status: "running", WorkflowSnapshot: wf, ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 3}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "pending"}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 1 {
		t.Fatalf("resumed: %v", resumed)
	}
	deadline := time.Now().Add(75 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.Run(run.ID)
		n := got.Nodes["n"]
		if n != nil && n.Status == "waiting_approval" && got.Status == "waiting" {
			if n.Attempt != 1 || n.ExecutionSequence != 3 || n.ActiveModel != "backup" || n.ModelErrorClass != "auth" || primary.Load() < 2 || backup.Load() == 0 {
				t.Fatalf("primary=%d backup=%d node=%+v", primary.Load(), backup.Load(), n)
			}
			retries, switched := 0, false
			for _, ev := range got.Events {
				if ev.Type == "model.retry_waiting" {
					retries++
				}
				if ev.Type == "model.fallback" && strings.Contains(ev.Message, "backup") {
					switched = true
				}
			}
			if retries != 2 || !switched {
				t.Fatalf("retries=%d fallback=%v", retries, switched)
			}
			t.Logf("primary HTTP=%d backup HTTP=%d attempt=%d executions=%d retries=%d fallback=%s final=%s", primary.Load(), backup.Load(), n.Attempt, n.ExecutionSequence, retries, n.ActiveModel, n.Status)
			return
		}
		if got.Status == "failed" {
			t.Fatalf("run failed: %+v", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := store.Run(run.ID)
	t.Fatalf("timed out: run=%s node=%+v primary=%d backup=%d", got.Status, got.Nodes["n"], primary.Load(), backup.Load())
}

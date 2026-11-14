package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestRealClaudeAndCodeBuddyEngineCredentialGate(t *testing.T) {
	if os.Getenv("LOOPFORGE_REAL_CLI_TEST") != "1" {
		t.Skip("set LOOPFORGE_REAL_CLI_TEST=1 to run installed CLIs")
	}
	for _, provider := range []struct{ binary, executor string }{{"claude", "claude"}, {"codebuddy", "codebuddy"}} {
		t.Run(provider.executor, func(t *testing.T) {
			if _, err := exec.LookPath(provider.binary); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid_api_key"}}`)
			}))
			defer srv.Close()
			t.Setenv("NO_PROXY", "localhost,127.0.0.1")
			t.Setenv("no_proxy", "localhost,127.0.0.1")
			t.Setenv("CLAUDE_CODE_MAX_RETRIES", "0")
			t.Setenv("CODEBUDDY_MAX_RETRIES", "0")
			t.Setenv("DISABLE_TELEMETRY", "1")
			t.Setenv("DISABLE_AUTOUPDATER", "1")
			t.Setenv("CODEBUDDY_BASE_URL", srv.URL+"/v1")
			e, store := newRecoveryEngine(t)
			if err := store.PutUser(domain.User{ID: 3, LoginName: "live-cli"}); err != nil {
				t.Fatal(err)
			}
			base := srv.URL
			if _, err := store.UpdateUserCredentials(3, repo.UserCredentialUpdate{ClaudeCodeAPIKey: "local-test-key", ClaudeCodeBaseURL: &base, CodeBuddyAPIKey: "local-test-key"}); err != nil {
				t.Fatal(err)
			}
			if err := store.PutAgent(domain.AgentDefinition{ID: "real-cli", Name: "Real CLI", ExecutorID: provider.executor, Model: "test-model"}); err != nil {
				t.Fatal(err)
			}
			wf := domain.WorkflowDefinition{ID: "cli-gate", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: "real-cli", MaxAttempts: 1}}}
			run := domain.Run{ID: "cli-gate-" + provider.executor, WorkflowID: wf.ID, Status: "running", WorkflowSnapshot: wf, ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 3}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "pending"}}}
			if err := store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if resumed := e.ResumeInterruptedRuns(); len(resumed) != 1 {
				t.Fatalf("resumed=%v", resumed)
			}
			deadline := time.Now().Add(45 * time.Second)
			for time.Now().Before(deadline) {
				got, _ := store.Run(run.ID)
				n := got.Nodes["n"]
				if n != nil && n.Status == "waiting_approval" && got.Status == "waiting" {
					if n.WaitReason != "model_credentials" || n.ModelErrorClass != "auth" || n.Attempt != 1 || n.ExecutionSequence != 1 || requests.Load() == 0 {
						t.Fatalf("requests=%d node=%+v", requests.Load(), n)
					}
					t.Logf("requests=%d status=%s reason=%s class=%s attempt=%d", requests.Load(), n.Status, n.WaitReason, n.ModelErrorClass, n.Attempt)
					return
				}
				if got.Status == "failed" {
					t.Fatalf("failed node=%+v", n)
				}
				time.Sleep(50 * time.Millisecond)
			}
			got, _ := store.Run(run.ID)
			t.Fatalf("timeout status=%s node=%+v requests=%d", got.Status, got.Nodes["n"], requests.Load())
		})
	}
}

func TestRealClaudeAndCodeBuddyEngineRateLimitFallback(t *testing.T) {
	if os.Getenv("LOOPFORGE_REAL_CLI_TEST") != "1" {
		t.Skip("set LOOPFORGE_REAL_CLI_TEST=1 to run installed CLIs")
	}
	for _, provider := range []struct{ binary, executor string }{{"claude", "claude"}, {"codebuddy", "codebuddy"}} {
		t.Run(provider.executor, func(t *testing.T) {
			if _, err := exec.LookPath(provider.binary); err != nil {
				t.Fatal(err)
			}
			var primary, backup atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode model: %v", err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch request.Model {
				case "primary":
					primary.Add(1)
					w.WriteHeader(429)
					fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`)
				case "backup":
					backup.Add(1)
					w.WriteHeader(401)
					fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid_api_key"}}`)
				default:
					t.Errorf("unexpected model %q", request.Model)
					w.WriteHeader(400)
				}
			}))
			defer srv.Close()
			t.Setenv("NO_PROXY", "localhost,127.0.0.1")
			t.Setenv("no_proxy", "localhost,127.0.0.1")
			t.Setenv("CLAUDE_CODE_MAX_RETRIES", "0")
			t.Setenv("CODEBUDDY_MAX_RETRIES", "0")
			t.Setenv("DISABLE_TELEMETRY", "1")
			t.Setenv("DISABLE_AUTOUPDATER", "1")
			t.Setenv("CODEBUDDY_BASE_URL", srv.URL+"/v1")
			e, store := newRecoveryEngine(t)
			if err := store.PutUser(domain.User{ID: 3, LoginName: "live-fallback"}); err != nil {
				t.Fatal(err)
			}
			base := srv.URL
			if _, err := store.UpdateUserCredentials(3, repo.UserCredentialUpdate{ClaudeCodeAPIKey: "local-test-key", ClaudeCodeBaseURL: &base, CodeBuddyAPIKey: "local-test-key"}); err != nil {
				t.Fatal(err)
			}
			if err := store.PutAgent(domain.AgentDefinition{ID: "real-cli", Name: "Real CLI", ExecutorID: provider.executor, Model: "primary", FallbackModels: []string{"backup"}}); err != nil {
				t.Fatal(err)
			}
			initial, maximum := 1, 1
			wf := domain.WorkflowDefinition{ID: "cli-fallback", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: "real-cli", MaxAttempts: 1, ModelRetry: &domain.ModelRetryPolicy{InitialSeconds: &initial, MaxSeconds: &maximum}}}}
			run := domain.Run{ID: "cli-fallback-" + provider.executor, WorkflowID: wf.ID, Status: "running", WorkflowSnapshot: wf, ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 3}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "pending"}}}
			if err := store.PutRun(run); err != nil {
				t.Fatal(err)
			}
			if resumed := e.ResumeInterruptedRuns(); len(resumed) != 1 {
				t.Fatalf("resumed=%v", resumed)
			}
			deadline := time.Now().Add(70 * time.Second)
			for time.Now().Before(deadline) {
				got, _ := store.Run(run.ID)
				n := got.Nodes["n"]
				if n != nil && n.Status == "waiting_approval" && got.Status == "waiting" {
					if n.ActiveModel != "backup" || n.ModelErrorClass != "auth" || n.Attempt != 1 || n.ExecutionSequence != 3 || primary.Load() < 2 || backup.Load() == 0 {
						t.Fatalf("primary=%d backup=%d node=%+v", primary.Load(), backup.Load(), n)
					}
					retries, fallback := 0, false
					for _, ev := range got.Events {
						if ev.Type == "model.retry_waiting" {
							retries++
						}
						if ev.Type == "model.fallback" {
							fallback = true
						}
					}
					if retries != 2 || !fallback {
						t.Fatalf("retries=%d fallback=%t", retries, fallback)
					}
					t.Logf("primary HTTP=%d backup HTTP=%d retry events=%d execution=%d attempt=%d fallback=%s", primary.Load(), backup.Load(), retries, n.ExecutionSequence, n.Attempt, n.ActiveModel)
					return
				}
				if got.Status == "failed" {
					t.Fatalf("failed node=%+v", n)
				}
				time.Sleep(50 * time.Millisecond)
			}
			got, _ := store.Run(run.ID)
			t.Fatalf("timeout status=%s node=%+v primary=%d backup=%d", got.Status, got.Nodes["n"], primary.Load(), backup.Load())
		})
	}
}

func TestRealClaudeEngineRateLimitThenGateSuccess(t *testing.T) {
	if os.Getenv("LOOPFORGE_REAL_CLI_TEST") != "1" {
		t.Skip("set LOOPFORGE_REAL_CLI_TEST=1 to run installed CLIs")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request: %v", err)
			return
		}
		n := requests.Add(1)
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded"}}`)
			return
		}
		tool := ""
		for _, v := range body.Tools {
			if v.Name == "Bash" {
				tool = v.Name
				break
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		write := func(name, value string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, value) }
		write("message_start", `{"type":"message_start","message":{"id":"msg_local","type":"message","role":"assistant","content":[],"model":"test-model","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`)
		if n == 3 && tool != "" {
			encoded, _ := json.Marshal(tool)
			write("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_local","name":%s,"input":{}}}`, encoded))
			// The fixture asks the real CLI to execute the gate server through its
			// Bash tool; it does not simulate a native MCP tool call or a live model.
			command := `printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_gate_result","arguments":{"decision":"pass","reason":"local controlled response"}}}' | env AGENTFLOW_GATE_RESULT_PATH=.agentflow/gate-results/n/attempt-1.json .agentflow/config/real-cli/gate-mcp`
			input, _ := json.Marshal(map[string]string{"command": command})
			delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": string(input)}})
			write("content_block_delta", string(delta))
			write("content_block_stop", `{"type":"content_block_stop","index":0}`)
			write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":10}}`)
		} else {
			if n == 3 {
				t.Errorf("Bash tool absent in request: %+v", body.Tools)
			}
			write("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`)
			write("content_block_stop", `{"type":"content_block_stop","index":0}`)
			write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`)
		}
		write("message_stop", `{"type":"message_stop"}`)
	}))
	defer srv.Close()
	t.Setenv("NO_PROXY", "localhost,127.0.0.1")
	t.Setenv("no_proxy", "localhost,127.0.0.1")
	t.Setenv("CLAUDE_CODE_MAX_RETRIES", "0")
	t.Setenv("DISABLE_TELEMETRY", "1")
	t.Setenv("DISABLE_AUTOUPDATER", "1")
	e, store := newRecoveryEngine(t)
	if err := store.PutUser(domain.User{ID: 3, LoginName: "live-gate"}); err != nil {
		t.Fatal(err)
	}
	base := srv.URL
	if _, err := store.UpdateUserCredentials(3, repo.UserCredentialUpdate{ClaudeCodeAPIKey: "local-test-key", ClaudeCodeBaseURL: &base}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAgent(domain.AgentDefinition{ID: "real-cli", Name: "Real CLI", ExecutorID: "claude", Model: "test-model"}); err != nil {
		t.Fatal(err)
	}
	initial, maximum := 1, 1
	wf := domain.WorkflowDefinition{ID: "cli-success", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "n", Type: "agent", AgentID: "real-cli", MaxAttempts: 1, ModelRetry: &domain.ModelRetryPolicy{InitialSeconds: &initial, MaxSeconds: &maximum}}}}
	run := domain.Run{ID: "claude-real-gate", WorkflowID: wf.ID, Status: "running", WorkflowSnapshot: wf, ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 3}, Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "pending"}}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if resumed := e.ResumeInterruptedRuns(); len(resumed) != 1 {
		t.Fatalf("resumed=%v", resumed)
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := store.Run(run.ID)
		n := got.Nodes["n"]
		if got.Status == "succeeded" {
			if n.Attempt != 1 || n.ExecutionSequence != 3 || n.GateResult == nil || n.GateResult.Decision != "pass" {
				t.Fatalf("node=%+v", n)
			}
			retries := 0
			for _, ev := range got.Events {
				if ev.Type == "model.retry_waiting" {
					retries++
				}
			}
			if retries != 2 {
				t.Fatalf("retry events=%d", retries)
			}
			t.Logf("HTTP requests=%d retries=%d attempt=%d execution=%d gate=%s status=%s", requests.Load(), retries, n.Attempt, n.ExecutionSequence, n.GateResult.Decision, got.Status)
			return
		}
		if got.Status == "failed" || got.Status == "waiting" {
			t.Fatalf("status=%s node=%+v requests=%d", got.Status, n, requests.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, _ := store.Run(run.ID)
	t.Fatalf("timeout status=%s node=%+v requests=%d", got.Status, got.Nodes["n"], requests.Load())
}

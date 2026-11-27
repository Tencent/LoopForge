package runner

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/managedagent"
	"agentflow/internal/repo"
)

func TestExternalA2ASOP(t *testing.T) {
	for _, target := range []string{domain.ExternalAgentRunner, domain.ExternalAgentRemote} {
		t.Run(target, func(t *testing.T) {
			api, store, eng := newDaemonTestServer(t)
			if err := store.PutProject(domain.Project{ID: "external-test", Key: "external-test", Name: "External Agent", OwnerID: 42}); err != nil {
				t.Fatal(err)
			}
			local := target == domain.ExternalAgentRunner
			expected := "A2A_" + strings.ToUpper(target) + "_OK"
			connector := externalAgentTestConnector(t, local, expected)
			if local {
				runner, _ := startPoolRunnerWithConfig(t, store, Config{
					ServerURL: api.URL, BootstrapToken: "boot-secret", Name: "personal-external-agent",
					StatePath: filepath.Join(t.TempDir(), "state.json"), DefaultWorkspaceRoot: t.TempDir(),
					HeartbeatInterval: time.Second, LongPollTimeout: 2 * time.Second,
					DisableAutoUpdate: true, ExternalAgent: connector,
				})
				runner.OwnerUserID = 42
				if err := store.PutRunner(runner); err != nil {
					t.Fatal(err)
				}
				if err := store.SetRunnerSharedProjects(runner.ID, 42, []string{"external-test"}); err != nil {
					t.Fatal(err)
				}
				if err := store.SetDefaultRunner(42, runner.ID, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			} else if err := eng.ConfigureManagedAgent(domain.A2AExecutorID, "external-test", 42, connector); err != nil {
				t.Fatal(err)
			}
			if !eng.ManagedAgentAvailable("external-test", 42, domain.A2AExecutorID) {
				t.Fatal("A2A Agent is missing from the role editor's availability check")
			}

			role := domain.AgentDefinition{ID: "custom-agent", Name: "Custom Agent", ExecutorID: domain.A2AExecutorID, ExternalAgentTarget: target, Prompt: "{{input}}"}
			if err := store.PutAgent(role); err != nil {
				t.Fatal(err)
			}
			wf := domain.WorkflowDefinition{
				ID: "external-agent-sop", Name: "External Agent LOOP", SandboxProfileID: repo.TestNativeSandboxID,
				Nodes: []domain.WorkflowNode{
					{ID: "agent", Name: "Agent", Type: "agent", AgentID: role.ID, MaxAttempts: 1},
				},
			}
			if err := store.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			requirements, err := eng.SOPExecutorRequirements(wf, 42, "external-test")
			if err != nil || len(requirements) != 1 || requirements[0].Kind != domain.A2AExecutorID || !requirements[0].Configured {
				t.Fatalf("unexpected LOOP prerequisites: %v, %v", requirements, err)
			}
			run, err := eng.StartWithExecutionContext(wf.ID, "Reply with exactly "+expected+" and nothing else.", domain.RepositoryContext{},
				domain.TelemetryContext{ProjectID: "external-test", ResponsibleUserID: 42})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, time.Minute, "external Agent LOOP", func() bool {
				current, _ := store.Run(run.ID)
				return current.Status == "succeeded" || current.Status == "failed"
			})
			final, _ := store.Run(run.ID)
			if final.Status != "succeeded" || strings.TrimSpace(final.Nodes["agent"].Output) != expected {
				t.Fatalf("LOOP status %s: Agent error %q", final.Status, final.Nodes["agent"].Error)
			}
			if len(final.Artifacts) != 1 || final.Artifacts[0].NodeID != "agent" || final.Artifacts[0].Path != "result.md" || strings.TrimSpace(final.Artifacts[0].Content) != expected {
				t.Fatalf("unexpected A2A result artifact: %+v", final.Artifacts)
			}
			for _, task := range store.RunnerTasks() {
				if task.Action != domain.RunnerTaskActionExternalAgent {
					continue
				}
				if !local || task.RunnerID != run.ExecutionContext.ExecutionRunnerID || task.Status != domain.RunnerTaskSuccess {
					t.Fatalf("unexpected external Agent task %s on %s", task.Status, task.RunnerID)
				}
				return
			}
			if local {
				t.Fatal("personal Agent did not receive a Runner task")
			}
		})
	}
}

func externalAgentTestConnector(t *testing.T, local bool, expected string) *managedagent.A2A {
	t.Helper()
	key := "a2a-test-only"
	var client *http.Client
	env := "LF_A2A_E2E_REMOTE"
	if local {
		env = "LF_A2A_E2E_RUNNER"
	}
	endpoint := os.Getenv(env + "_URL")
	if endpoint == "" {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+key || r.Method != http.MethodPost {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var request struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			text := []map[string]string{{"kind": "text", "text": expected}}
			var result any
			switch request.Method {
			case "message/send":
				if !strings.Contains(string(request.Params), expected) {
					t.Error("the task prompt is missing the test request")
				}
				if local {
					result = map[string]any{"kind": "task", "id": "async-task", "status": map[string]string{"state": "working"}}
				} else {
					result = map[string]any{"kind": "message", "role": "agent", "parts": text}
				}
			case "tasks/get":
				result = map[string]any{
					"kind": "task", "id": "async-task", "status": map[string]string{"state": "completed"},
					"artifacts": []any{map[string]any{"parts": text}},
				}
			default:
				t.Errorf("unexpected Agent method %s", request.Method)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
		})
		var server *httptest.Server
		if local {
			server = httptest.NewServer(handler)
		} else {
			server = httptest.NewTLSServer(handler)
		}
		t.Cleanup(server.Close)
		endpoint, client = server.URL, server.Client()
	} else {
		key = os.Getenv(env + "_TOKEN")
		if ca := os.Getenv(env + "_CA"); ca != "" {
			pem, err := os.ReadFile(ca)
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(pem) {
				t.Fatal("invalid test CA")
			}
			client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
		}
	}
	connector, err := managedagent.NewA2A(managedagent.A2AConfig{Endpoint: endpoint, Token: key, Loopback: local}, client)
	if err != nil {
		t.Fatal(err)
	}
	return connector
}

// TestExternalA2ATurnTimeoutIsNotResent covers a turn that runs past its
// budget on either target. The prompt already reached the Agent and the cancel
// is advisory, so the node must stop on the current attempt instead of sending
// the same work again: for a personal Runner the "unknown outcome" code has to
// survive the completion API, where the error value does not.
func TestExternalA2ATurnTimeoutIsNotResent(t *testing.T) {
	for _, target := range []string{domain.ExternalAgentRunner, domain.ExternalAgentRemote} {
		t.Run(target, func(t *testing.T) {
			var sends, cancels atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var result any
				switch request.Method {
				case "message/send":
					sends.Add(1)
					result = map[string]any{"kind": "task", "id": "stuck-task", "status": map[string]string{"state": "working"}}
				case "tasks/get":
					result = map[string]any{"kind": "task", "id": "stuck-task", "status": map[string]string{"state": "working"}}
				case "tasks/cancel":
					cancels.Add(1)
					result = map[string]any{"kind": "task", "id": "stuck-task", "status": map[string]string{"state": "canceled"}}
				default:
					t.Errorf("unexpected Agent method %s", request.Method)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
			})
			var server *httptest.Server
			if target == domain.ExternalAgentRunner {
				server = httptest.NewServer(handler)
			} else {
				server = httptest.NewTLSServer(handler)
			}
			t.Cleanup(server.Close)
			connector, err := managedagent.NewA2A(managedagent.A2AConfig{
				Endpoint: server.URL, Token: "a2a-test-only", Loopback: target == domain.ExternalAgentRunner,
			}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			previous := managedagent.TurnTimeout()
			managedagent.SetTurnTimeout(1500 * time.Millisecond)
			t.Cleanup(func() { managedagent.SetTurnTimeout(previous) })

			api, store, eng := newDaemonTestServer(t)
			if err := store.PutProject(domain.Project{ID: "turn-timeout", Key: "turn-timeout", Name: "Turn Timeout", OwnerID: 42}); err != nil {
				t.Fatal(err)
			}
			if target == domain.ExternalAgentRunner {
				runner, _ := startPoolRunnerWithConfig(t, store, Config{
					ServerURL: api.URL, BootstrapToken: "boot-secret", Name: "personal-turn-timeout",
					StatePath: filepath.Join(t.TempDir(), "state.json"), DefaultWorkspaceRoot: t.TempDir(),
					HeartbeatInterval: time.Second, LongPollTimeout: 2 * time.Second,
					DisableAutoUpdate: true, ExternalAgent: connector,
				})
				runner.OwnerUserID = 42
				if err := store.PutRunner(runner); err != nil {
					t.Fatal(err)
				}
				if err := store.SetRunnerSharedProjects(runner.ID, 42, []string{"turn-timeout"}); err != nil {
					t.Fatal(err)
				}
				if err := store.SetDefaultRunner(42, runner.ID, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			} else if err := eng.ConfigureManagedAgent(domain.A2AExecutorID, "turn-timeout", 42, connector); err != nil {
				t.Fatal(err)
			}
			role := domain.AgentDefinition{ID: "stuck-agent", Name: "Stuck Agent", ExecutorID: domain.A2AExecutorID, ExternalAgentTarget: target, Prompt: "{{input}}"}
			if err := store.PutAgent(role); err != nil {
				t.Fatal(err)
			}
			wf := domain.WorkflowDefinition{
				ID: "turn-timeout-sop", Name: "Turn Timeout LOOP", SandboxProfileID: repo.TestNativeSandboxID,
				Nodes: []domain.WorkflowNode{{ID: "agent", Name: "Agent", Type: "agent", AgentID: role.ID, MaxAttempts: 3}},
			}
			if err := store.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			run, err := eng.StartWithExecutionContext(wf.ID, "do the work", domain.RepositoryContext{},
				domain.TelemetryContext{ProjectID: "turn-timeout", ResponsibleUserID: 42})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, 90*time.Second, "turn timeout LOOP", func() bool {
				current, _ := store.Run(run.ID)
				return current.Status == "succeeded" || current.Status == "failed"
			})
			final, _ := store.Run(run.ID)
			node := final.Nodes["agent"]
			if final.Status != "failed" {
				t.Fatalf("LOOP status %q, want failed (node error %q)", final.Status, node.Error)
			}
			if got := sends.Load(); got != 1 {
				t.Fatalf("message/send count = %d, want 1: an unknown turn must not be resent", got)
			}
			if got := node.Attempt; got != 1 {
				t.Fatalf("final attempt = %d, want 1: a timed-out turn must not consume the remaining attempts", got)
			}
			if got := cancels.Load(); got != 1 {
				t.Fatalf("tasks/cancel count = %d, want 1", got)
			}
			if !strings.Contains(node.Error, "未在") || !strings.Contains(node.Error, "请求取消") {
				t.Fatalf("node error %q does not explain the timeout", node.Error)
			}
		})
	}
}

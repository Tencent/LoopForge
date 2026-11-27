package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	_ "unsafe"

	"agentflow/internal/domain"
	"agentflow/internal/managedagent"
	"agentflow/internal/repo"
)

//go:linkname linkedTurnTimeout agentflow/internal/managedagent.turnTimeout

// linkedTurnTimeout shortens the managed Agent turn budget for this test.
var linkedTurnTimeout time.Duration

// TestExternalA2ARunnerTurnTimeoutDoesNotResend guards the review finding on
// PR #1469: a personal-Runner A2A turn that runs past the turn budget has an
// unknown remote outcome, so the node must fail on its first attempt instead
// of the engine resending the same prompt on the next one.
func TestExternalA2ARunnerTurnTimeoutDoesNotResend(t *testing.T) {
	var sends atomic.Int32
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		var result any
		switch request.Method {
		case "message/send":
			sends.Add(1)
			result = map[string]any{"kind": "task", "id": "slow-task", "status": map[string]string{"state": "working"}}
		case "tasks/get":
			result = map[string]any{"kind": "task", "id": "slow-task", "status": map[string]string{"state": "working"}}
		default:
			result = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer agent.Close()

	previous := linkedTurnTimeout
	linkedTurnTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { linkedTurnTimeout = previous })

	api, store, eng := newDaemonTestServer(t)
	if err := store.PutProject(domain.Project{ID: "external-test", Key: "external-test", Name: "External Agent", OwnerID: 42}); err != nil {
		t.Fatal(err)
	}
	connector, err := managedagent.NewA2A(managedagent.A2AConfig{Endpoint: agent.URL, Loopback: true}, agent.Client())
	if err != nil {
		t.Fatal(err)
	}
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
	role := domain.AgentDefinition{ID: "custom-agent", Name: "Custom Agent", ExecutorID: domain.A2AExecutorID, ExternalAgentTarget: domain.ExternalAgentRunner, Prompt: "{{input}}"}
	if err := store.PutAgent(role); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "external-agent-timeout", Name: "External Agent LOOP", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{{ID: "agent", Name: "Agent", Type: "agent", AgentID: role.ID, MaxAttempts: 2}},
	}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := eng.StartWithExecutionContext(wf.ID, "never finishes", domain.RepositoryContext{},
		domain.TelemetryContext{ProjectID: "external-test", ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 90*time.Second, "runner A2A timeout LOOP", func() bool {
		current, _ := store.Run(run.ID)
		return current.Status == "succeeded" || current.Status == "failed"
	})
	final, _ := store.Run(run.ID)
	node := final.Nodes["agent"]
	if final.Status != "failed" || node.Attempt != 1 {
		t.Fatalf("LOOP %s attempt %d: a timed-out turn must fail on its first attempt (error %q)", final.Status, node.Attempt, node.Error)
	}
	if !strings.Contains(node.Error, "未在 1.5s 内完成本轮") {
		t.Fatalf("node error %q does not name the turn timeout", node.Error)
	}
	if got := sends.Load(); got != 1 {
		t.Fatalf("message/send count = %d, want 1 (a timed-out turn must not be resent)", got)
	}
}

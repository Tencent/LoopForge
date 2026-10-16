package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestWorkspaceControlWorkerForwardsThroughInternalAPI(t *testing.T) {
	// Worker has no Runner WebSocket hub; the API owns the outbound connection.
	worker, st := newRecoveryEngine(t)
	api := New(st, runtime.NewManager(t.TempDir()))
	api.ConfigureControl(ControlConfig{Token: "shared-control"})
	run := domain.Run{ID: "control-run", Status: "running", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner", RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared}, WorkflowSnapshot: preparationWorkflow(), Nodes: map[string]*domain.NodeRun{"env": {NodeID: "env", Status: "succeeded"}, "index": {NodeID: "index", Status: "succeeded"}, "work": {NodeID: "work", Status: "running"}}}
	if err := st.PutRunner(domain.Runner{ID: "runner", RunnerVersion: domain.RunnerVersionWorkspacePreparation}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	operations := make(chan string, 3)
	api.WorkspaceCheckpointTransport = func(_ context.Context, actual domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if actual.ID != run.ID || req.Workflow == nil || req.Sandbox == nil || req.RunStatus != "running" || req.WorkspaceLayout != domain.RunnerWorkspaceLayoutShared {
			t.Errorf("unbound request: %+v", req)
		}
		operations <- req.Operation
		payload, _ := json.Marshal(runtime.WorkspacePreparationStatus{Status: "ready", Steps: []runtime.WorkspacePreparationStep{}})
		return domain.RunnerWorkspaceResponse{Payload: payload}, nil
	}
	server := httptest.NewServer(http.HandlerFunc(api.HandleWorkspaceCheckpoint))
	defer server.Close()
	worker.ConfigureControl(ControlConfig{Role: "worker", APIURL: server.URL + "/", Token: "shared-control"})
	worker.ConfigurePlatformMCP("http://sandbox-only.invalid:9999", "different-mcp-token")
	for _, operation := range []string{domain.RunnerWorkspaceOperationPrepare, domain.RunnerWorkspaceOperationPreparationStatus, domain.RunnerWorkspaceOperationPreparationRecord} {
		req := domain.RunnerWorkspaceRequest{RunID: run.ID, Operation: operation, PreparationNodeID: "env"}
		response, err := worker.requestWorkspaceCheckpoint(context.Background(), run, req)
		if err != nil || response.Error != "" || len(response.Payload) == 0 {
			t.Fatalf("%s: %+v %v", operation, response, err)
		}
	}
	if len(operations) != 3 {
		t.Fatal("workspace requests were not all forwarded")
	}
}

func TestWorkspaceControlConfigurationErrorsAreSpecific(t *testing.T) {
	e, _ := newRecoveryEngine(t)
	run := domain.Run{ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner"}}
	for _, tc := range []struct{ name, base, token, operation, want string }{
		{"missing token", "http://127.0.0.1:1", "", domain.RunnerWorkspaceOperationPrepare, "AGENTFLOW_CONTROL_TOKEN"},
		{"missing internal address", "", "configured", domain.RunnerWorkspaceOperationPrepare, "AGENTFLOW_API_URL"},
		{"save operation", "http://127.0.0.1:1", "", domain.RunnerWorkspaceOperationCheckpoint, "保存工作区快照"},
		{"restore operation", "http://127.0.0.1:1", "", domain.RunnerWorkspaceOperationRestore, "恢复工作区快照"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.ConfigureControl(ControlConfig{APIURL: tc.base, Token: tc.token})
			e.ConfigurePlatformMCP("http://sandbox-only.invalid", "mcp-token")
			_, err := e.requestWorkspaceCheckpoint(context.Background(), run, domain.RunnerWorkspaceRequest{Operation: tc.operation})
			if !errors.Is(err, ErrWorkspaceControlConfiguration) || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "暂缓回收") {
				t.Fatal(err)
			}
		})
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "not authorized", status) }))
			defer server.Close()
			e.ConfigureControl(ControlConfig{APIURL: server.URL, Token: "configured"})
			_, err := e.requestWorkspaceCheckpoint(context.Background(), run, domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationPrepare})
			if !errors.Is(err, ErrWorkspaceControlConfiguration) || !strings.Contains(err.Error(), "认证失败") {
				t.Fatal(err)
			}
		})
	}
	// The authenticated in-process API -> Runner connection needs no HTTP token.
	e.ConfigureControl(ControlConfig{})
	e.WorkspaceCheckpointTransport = func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		return domain.RunnerWorkspaceResponse{Payload: json.RawMessage(`{"status":"ready"}`)}, nil
	}
	if _, err := e.requestWorkspaceCheckpoint(context.Background(), run, domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationPrepare}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceControlFailureDoesNotRetryOrRejectUpstream(t *testing.T) {
	e, st := newRecoveryEngine(t)
	node := domain.WorkflowNode{ID: "design", MaxAttempts: 8, RejectTo: "env", MaxReworks: 3}
	run := domain.Run{ID: "control-failed", Status: "running", Generation: 1, WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "env"}, node}}, Nodes: map[string]*domain.NodeRun{"env": {NodeID: "env", Status: "succeeded", Attempt: 1}, "design": {NodeID: "design", Status: "running", Attempt: 1, StartedAt: time.Now()}}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	failure := fmt.Errorf("prepare: %w", ErrWorkspaceControlConfiguration)
	e.nodeFailedAt(run.ID, node, 1, 1, failure, true)
	got, _ := st.RunState(run.ID)
	if got.Nodes["design"].Status != "failed" || got.Nodes["design"].Attempt != 1 || got.Nodes["env"].Status != "succeeded" || got.Nodes["env"].ReworkCount != 0 {
		t.Fatalf("configuration failure retried or rejected: %+v", got.Nodes)
	}
	for _, ev := range got.Events {
		if ev.Type == "node.retry" || ev.Type == "node.rejected" {
			t.Fatal(ev)
		}
	}
	// A historical rework verdict must not turn an infrastructure failure into
	// a new human approval when the earlier node's rework budget is exhausted.
	got.Nodes["design"].Status = "running"
	got.Nodes["design"].GateResult = &domain.GateResult{Decision: "rework"}
	got.Nodes["env"].ReworkCount = 3
	if err := st.PutRun(got); err != nil {
		t.Fatal(err)
	}
	e.nodeFailedAt(run.ID, node, 1, 1, failure, true)
	got, _ = st.RunState(run.ID)
	if got.Nodes["design"].Status != "failed" || got.Nodes["env"].ReworkCount != 3 {
		t.Fatal("configuration error was treated as a rework verdict")
	}
	if shouldRetryFailedAttempt(node, 1, failure) {
		t.Fatal("configuration error is retryable")
	}
	if !shouldRetryFailedAttempt(node, 1, fmt.Errorf("temporary executor failure")) {
		t.Fatal("ordinary retry behavior changed")
	}
}

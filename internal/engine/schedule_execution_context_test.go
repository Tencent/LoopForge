package engine

import (
	"encoding/json"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func executionAttributionContext(t *testing.T, id string) domain.TelemetryContext {
	t.Helper()
	data, err := json.Marshal(map[string]string{"scheduleJobId": "job", "scheduleExecutionId": id})
	if err != nil {
		t.Fatal(err)
	}
	var ctx domain.TelemetryContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		t.Fatal(err)
	}
	return ctx
}

func executionAttributionID(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		ID string `json:"scheduleExecutionId"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.ID
}

func TestScheduledExecutionContextSurvivesChildSOP(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	child := domain.WorkflowDefinition{ID: "execution-child", Name: "Child", SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{{ID: "gate", Type: "approval"}}}
	parent := domain.WorkflowDefinition{ID: "execution-parent", Name: "Parent", SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{{ID: "sub", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1}}}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err := st.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	e := New(st, runtime.NewManagerWithLimit(t.TempDir(), 2))
	want := "sjx-occurrence-one"
	run, err := e.StartWithRepositoriesContext(parent.ID, "x", nil, executionAttributionContext(t, want))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(run.ID) })
	started := waitRun(t, st, run.ID, 5*time.Second, func(r domain.Run) bool {
		return r.Nodes["sub"] != nil && r.Nodes["sub"].ChildRunID != ""
	})
	childRun := waitRun(t, st, started.Nodes["sub"].ChildRunID, 5*time.Second, func(r domain.Run) bool { return r.Status == "waiting" })
	for _, r := range []domain.Run{started, childRun} {
		if got := executionAttributionID(t, r.ExecutionContext); got != want {
			t.Errorf("run %s lost schedule execution: %q", r.ID, got)
		}
		if r.ExecutionContext.TraceID != r.ID {
			t.Errorf("Run trace changed: %+v", r.ExecutionContext)
		}
	}
	if childRun.ExecutionContext.ParentRunID != run.ID {
		t.Fatal("child did not inherit its parent")
	}
}

func TestScheduledExecutionContextOverridesWorkerClaims(t *testing.T) {
	for _, want := range []string{"sjx-authoritative", ""} {
		t.Run(want, func(t *testing.T) {
			e, st := cvEEngine(t)
			run := domain.Run{ID: "attribution-worker", Status: "running", WorkflowID: "wf", ExecutionContext: executionAttributionContext(t, want), Nodes: map[string]*domain.NodeRun{}}
			if err := st.PutRun(run); err != nil {
				t.Fatal(err)
			}
			source := domain.TelemetrySource{Layer: "executor", Adapter: "test"}
			for _, envelope := range []domain.WorkerEnvelope{
				{SpecVersion: domain.ObservabilitySpecVersion, Type: "observation", Observation: &domain.TelemetryEvent{Name: "worker.example", Source: source, Context: executionAttributionContext(t, "sjx-forged")}},
				{SpecVersion: domain.ObservabilitySpecVersion, Type: "log", Log: &domain.LogRecord{Body: "worker example", Source: source, Context: executionAttributionContext(t, "sjx-forged")}},
			} {
				if err := e.IngestWorkerEnvelope(run.ID, envelope); err != nil {
					t.Fatal(err)
				}
			}
			got, _ := st.Run(run.ID)
			if len(got.Telemetry) == 0 || len(got.Logs) == 0 {
				t.Fatal("worker evidence was not persisted")
			}
			for _, event := range got.Telemetry {
				if id := executionAttributionID(t, event.Context); id != want {
					t.Errorf("telemetry attribution = %q, want %q", id, want)
				}
			}
			for _, log := range got.Logs {
				if id := executionAttributionID(t, log.Context); id != want {
					t.Errorf("log attribution = %q, want %q", id, want)
				}
			}
		})
	}
}

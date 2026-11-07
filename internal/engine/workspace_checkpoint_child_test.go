package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSharedChildWorkspaceControlPreservesOwnerAndSnapshotProtection(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := New(s, runtime.NewManager(t.TempDir()))
	e.ConfigureControl(ControlConfig{Token: "test-control-token"})
	if err = s.PutRunner(domain.Runner{ID: "runner", RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation, Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	child := domain.Run{ID: "child", Status: "running", ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner", RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared, WorkspaceRunID: "parent"}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "child-flow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "prepare", Type: "script", Script: "true", RunOnWorkspaceRebuild: true}}}, Nodes: map[string]*domain.NodeRun{}}
	if err = s.PutRun(child); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		operation string
		want      int
	}{
		{domain.RunnerWorkspaceOperationPrepare, 200},
		{domain.RunnerWorkspaceOperationPreparationStatus, 200},
		{domain.RunnerWorkspaceOperationPreparationRecord, 200},
		{domain.RunnerWorkspaceOperationTerminal, 200},
		{domain.RunnerWorkspaceOperationCheckpointExport, 409},
		{domain.RunnerWorkspaceOperationRestore, 409},
		{domain.RunnerWorkspaceOperationReclaim, 409},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			forwarded := false
			e.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				forwarded = true
				if req.RunID != child.ID || req.WorkspaceRunID != "parent" || req.Sandbox == nil || req.Workflow == nil {
					t.Errorf("child identity or trusted workspace owner lost: %+v", req)
				}
				return domain.RunnerWorkspaceResponse{Status: 200, Payload: json.RawMessage(`{"status":"ready"}`)}, nil
			}
			request := domain.RunnerWorkspaceRequest{RunID: child.ID, WorkspaceRunID: "untrusted", Operation: tt.operation, PreparationNodeID: "prepare", Terminal: &domain.WorkspaceTerminalRequest{Operation: "open"}, TerminalOwner: 1}
			body, _ := json.Marshal(request)
			req := httptest.NewRequest("POST", "/internal/workspace-checkpoint", bytes.NewReader(body))
			req.Header.Set(controlTokenHeader, "test-control-token")
			w := httptest.NewRecorder()
			e.HandleWorkspaceCheckpoint(w, req)
			if w.Code != tt.want || forwarded != (tt.want == 200) {
				t.Fatalf("status=%d forwarded=%v, want=%d: %s", w.Code, forwarded, tt.want, w.Body.String())
			}
		})
	}
}

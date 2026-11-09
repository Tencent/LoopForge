package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRunReadAnalyticsAndActionBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	now := time.Now().UTC()
	run := domain.Run{
		ID: "cvb-run-more", WorkflowID: "wf", Status: "succeeded", Input: strings.Repeat("i", 700),
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ResponsibleUserID: f.user.ID},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Name: "Workflow", CollaborationMode: "pipeline",
			Nodes: []domain.WorkflowNode{{ID: "n", Name: "Node", Type: "script"}}},
		Nodes:     map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "succeeded", Output: "done"}},
		Events:    []domain.RunEvent{{NodeID: "n", Type: "done", Message: "finished", Timestamp: now}},
		Logs:      []domain.LogRecord{{Sequence: 1, Timestamp: now, Stream: "stdout", Body: "hello", Context: domain.TelemetryContext{NodeID: "n"}}},
		Telemetry: []domain.TelemetryEvent{{Sequence: 1, Timestamp: now, Name: "activity", Context: domain.TelemetryContext{NodeID: "n"}}},
		Artifacts: []domain.Artifact{{ID: "a", Name: "report", MediaType: "text/markdown", Content: "# report", SHA256: "abc"}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/analytics?from=bad", nil, 400},
		{http.MethodGet, "/api/analytics?to=bad", nil, 400},
		{http.MethodGet, "/api/analytics?origin=bad", nil, 400},
		{http.MethodGet, "/api/analytics?origin=all&requirementsOnly=1", nil, 200},
		{http.MethodGet, "/api/workflow-metrics?projectId=platform", nil, 200},
		{http.MethodGet, "/api/runs?responsible=me&projectId=platform", nil, 200},
		{http.MethodGet, "/api/runs/cvb-run-more", nil, 200},
		{http.MethodGet, "/api/runs/missing", nil, 404},
		{http.MethodGet, "/api/runs/missing/telemetry", nil, 404},
		{http.MethodGet, "/api/runs/cvb-run-more/telemetry?expanded=true&after=0&limit=1", nil, 200},
		{http.MethodGet, "/api/runs/missing/logs", nil, 404},
		{http.MethodGet, "/api/runs/cvb-run-more/logs?nodeId=n&before=99&limit=1", nil, 200},
		{http.MethodGet, "/api/runs/cvb-run-more/logs.txt?nodeId=n", nil, 200},
		{http.MethodGet, "/api/runs/missing/logs.txt", nil, 404},
		{http.MethodGet, "/api/runs/cvb-run-more/artifacts/a", nil, 200},
		{http.MethodGet, "/api/runs/cvb-run-more/artifacts/missing", nil, 404},
		{http.MethodPost, "/api/runs/missing/approve", map[string]any{"nodeId": "n"}, 400},
		{http.MethodPost, "/api/runs/missing/reject", map[string]any{"nodeId": "n"}, 400},
		{http.MethodPost, "/api/runs/missing/decline-retry", map[string]any{"nodeId": "n"}, 400},
		{http.MethodPost, "/api/runs/missing/cancel", nil, 400},
		{http.MethodPost, "/api/runs/missing/pause", nil, 400},
		{http.MethodPost, "/api/runs/missing/refresh-sop", nil, 400},
		{http.MethodPost, "/api/runs/missing/pass", map[string]any{"nodeId": "n"}, 400},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

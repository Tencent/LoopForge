package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// TestRunCleanupAPI covers the retention candidate listing and the confirmed
// batch deletion used by the run list "清理旧运行" panel.
func TestRunCleanupAPI(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	f := cvBNewHTTPFixture(t)
	old := time.Now().UTC().AddDate(0, 0, -40)
	recent := time.Now().UTC().AddDate(0, 0, -2)
	workspaceRoot := f.e.WorkspaceRoot()
	oldWorkspace := filepath.Join(workspaceRoot, "run-cleanup-old")
	if err := os.MkdirAll(oldWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldWorkspace, "artifact.md"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := domain.TelemetryContext{ProjectID: "cleanup-project"}
	for _, run := range []domain.Run{
		{ID: "run-cleanup-old", Status: "succeeded", CreatedAt: old, UpdatedAt: old, WorkspaceRoot: oldWorkspace, ExecutionContext: ctx, WorkflowSnapshot: domain.WorkflowDefinition{Name: "SOP"}, Nodes: map[string]*domain.NodeRun{}},
		{ID: "run-cleanup-recent", Status: "succeeded", CreatedAt: recent, UpdatedAt: recent, ExecutionContext: ctx, WorkflowSnapshot: domain.WorkflowDefinition{Name: "SOP"}, Nodes: map[string]*domain.NodeRun{}},
		{ID: "run-cleanup-active", Status: "running", CreatedAt: old, UpdatedAt: old, ExecutionContext: ctx, Nodes: map[string]*domain.NodeRun{}},
	} {
		if err := f.s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}

	resp := cvBRequest(t, f, http.MethodGet, "/api/run-cleanup/candidates?olderThanDays=30", nil, true)
	if resp.Code != http.StatusOK {
		t.Fatalf("candidates: HTTP %d (%s)", resp.Code, resp.Body.String())
	}
	var view runCleanupView
	if err := json.Unmarshal(resp.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Totals.CandidateCount != 1 || len(view.Candidates) != 1 || view.Candidates[0].ID != "run-cleanup-old" {
		t.Fatalf("candidates = %+v", view)
	}
	if view.Candidates[0].WorkspaceBytes < 2048 {
		t.Fatalf("workspace bytes = %d, want >= 2048", view.Candidates[0].WorkspaceBytes)
	}
	if view.Totals.RetentionDays != 30 || view.Totals.DiskTotalBytes <= 0 {
		t.Fatalf("totals = %+v", view.Totals)
	}

	resp = cvBRequest(t, f, http.MethodPost, "/api/run-cleanup", map[string]any{
		"ids": []string{"run-cleanup-old", "run-cleanup-active", "run-cleanup-missing"},
	}, true)
	if resp.Code != http.StatusOK {
		t.Fatalf("cleanup: HTTP %d (%s)", resp.Code, resp.Body.String())
	}
	var result runCleanupResult
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0] != "run-cleanup-old" {
		t.Fatalf("deleted = %v, failed = %v", result.Deleted, result.Failed)
	}
	if len(result.Failed) != 2 {
		t.Fatalf("failed = %v, want active + missing", result.Failed)
	}
	if result.FreedBytes < 2048 {
		t.Fatalf("freed bytes = %d, want >= 2048", result.FreedBytes)
	}
	if _, ok := f.s.RunState("run-cleanup-old"); ok {
		t.Fatal("deleted run is still present")
	}
	if _, ok := f.s.RunState("run-cleanup-recent"); !ok {
		t.Fatal("recent run must be kept")
	}
}

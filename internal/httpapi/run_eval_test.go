package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestRunEvalRequiresTerminalRunAndUserKey(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "run-eval-session-secret")
	h, store, user := newRunEvalHarness(t)
	if err := store.PutRun(domain.Run{ID: "run-live", Status: "running", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	requestJSONAuthed[map[string]string](t, h, user, http.MethodPost, "/api/runs/run-live/eval", map[string]any{}, http.StatusConflict)

	if err := store.PutRun(domain.Run{ID: "run-done", Status: "succeeded", WorkflowID: "wf-eval", Nodes: map[string]*domain.NodeRun{"fix": {Status: "succeeded", Attempt: 1}}}); err != nil {
		t.Fatal(err)
	}
	if status := postJSONStatus(t, h, http.MethodPost, "/api/runs/run-done/eval", map[string]any{}); status != http.StatusUnauthorized {
		t.Fatalf("guest status=%d", status)
	}
	nokey := domain.User{StaffID: 3102, LoginName: "nokey", ChineseName: "无钥匙"}
	if err := store.PutUser(nokey); err != nil {
		t.Fatal(err)
	}
	nokey, _ = store.UserByLoginName("nokey")
	requestJSONAuthed[map[string]string](t, h, nokey, http.MethodPost, "/api/runs/run-done/eval", map[string]any{}, http.StatusBadRequest)
}

func TestRunEvalScoresAndAppliesNodePatch(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "run-eval-session-secret")
	h, store, user := newRunEvalHarness(t)
	if err := store.PutWorkflow(domain.WorkflowDefinition{ID: "wf-eval", Name: "评测SOP", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "fix", Name: "修复", Type: "agent", AgentID: "fixer", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAgent(domain.AgentDefinition{ID: "fixer", Name: "修复者", ProjectID: "platform", Instructions: "修 bug"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRun(domain.Run{
		ID: "run-done", Status: "succeeded", WorkflowID: "wf-eval",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-eval", Nodes: []domain.WorkflowNode{{ID: "fix", AgentID: "fixer", MaxAttempts: 1}}},
		Nodes:            map[string]*domain.NodeRun{"fix": {Status: "succeeded", Attempt: 3, ReworkCount: 2}},
		Telemetry:        []domain.TelemetryEvent{{Sequence: 1, NodeID: "fix", Category: "lifecycle", Name: "node.start", Phase: "start"}},
	}); err != nil {
		t.Fatal(err)
	}
	setRunEvalExecForTest(func(ctx context.Context, rec domain.RunEvalRecord, run domain.Run, u domain.User, emit func(string, any)) (domain.RunEvalResult, error) {
		return domain.RunEvalResult{
			Score:      55,
			Passed:     false,
			Dimensions: map[string]int{"control": 40, "latency": 80, "tools": 50, "tokens": 70, "recovery": 30, "delivery": 90},
			Summary:    "返工过多",
			Recommendations: []domain.RunEvalRecommendation{{
				ID: "rec-1", Title: "提高重试", Target: "workflow", TargetID: "wf-eval",
				Action: "update_node", NodeID: "fix", Set: map[string]any{"maxAttempts": 3}, Status: domain.RunEvalRecPending,
			}},
		}, nil
	})
	t.Cleanup(func() { setRunEvalExecForTest(nil) })

	started := requestJSONAuthed[domain.RunEvalRecord](t, h, user, http.MethodPost, "/api/runs/run-done/eval", map[string]any{"model": "default"}, http.StatusAccepted)
	if started.Status != domain.RunEvalRunning {
		t.Fatalf("start = %+v", started)
	}
	deadline := time.Now().Add(2 * time.Second)
	var latest domain.RunEvalRecord
	for time.Now().Before(deadline) {
		latest = requestJSONAuthed[domain.RunEvalRecord](t, h, user, http.MethodGet, "/api/runs/run-done/eval", nil, http.StatusOK)
		if latest.Status == domain.RunEvalSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if latest.Status != domain.RunEvalSucceeded || latest.Score != 55 || len(latest.Result.Recommendations) != 1 {
		t.Fatalf("eval = %+v", latest)
	}
	applied := requestJSONAuthed[domain.RunEvalRecord](t, h, user, http.MethodPost, "/api/runs/run-done/eval/apply", map[string]any{"evalId": latest.ID, "recommendationIds": []string{"rec-1"}}, http.StatusOK)
	if applied.Result.Recommendations[0].Status != domain.RunEvalRecApplied {
		t.Fatalf("apply = %+v", applied.Result.Recommendations[0])
	}
	wf, _ := store.Workflow("wf-eval")
	if wf.Nodes[0].MaxAttempts != 3 {
		t.Fatalf("workflow maxAttempts=%d", wf.Nodes[0].MaxAttempts)
	}
}

func newRunEvalHarness(t *testing.T) (http.Handler, *repo.Store, domain.User) {
	t.Helper()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.PutUser(domain.User{StaffID: 3101, LoginName: "evaler", ChineseName: "评测员"}); err != nil {
		t.Fatal(err)
	}
	user, _ := store.UserByLoginName("evaler")
	if _, err := store.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "ck_eval"}); err != nil {
		t.Fatal(err)
	}
	user, _ = store.User(user.ID)
	h := New(store, engine.New(store, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	return h, store, user
}

func postJSONStatus(t *testing.T, h http.Handler, method, path string, body any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

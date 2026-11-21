package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
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
	refusal := requestJSONAuthed[map[string]string](t, h, nokey, http.MethodPost, "/api/runs/run-done/eval", map[string]any{}, http.StatusBadRequest)
	if !strings.Contains(refusal["error"], "CodeBuddy API Key") {
		t.Fatalf("default model must still report the CodeBuddy key: %v", refusal)
	}
}

func TestRunEvalNonCodeBuddyRuntimeUsesItsOwnCredential(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "run-eval-session-secret")
	h, store, _ := newRunEvalHarness(t)
	if err := store.PutRun(domain.Run{
		ID: "run-pi", Status: "succeeded", WorkflowID: "wf-eval",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf-eval", Nodes: []domain.WorkflowNode{{ID: "fix", AgentID: "fixer"}}},
		Nodes:            map[string]*domain.NodeRun{"fix": {Status: "succeeded", Attempt: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUser(domain.User{StaffID: 3103, LoginName: "pionly", ChineseName: "仅 Pi"}); err != nil {
		t.Fatal(err)
	}
	piUser, _ := store.UserByLoginName("pionly")
	if _, err := store.UpdateUserCredentials(piUser.ID, repo.UserCredentialUpdate{AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{
		"pi": {APIKey: "pi-eval-key"},
	}}); err != nil {
		t.Fatal(err)
	}
	piUser, _ = store.User(piUser.ID)

	type cliCall struct {
		req  assistantCLIRequest
		user domain.User
	}
	calls := make(chan cliCall, 1)
	// Fake CLI at the runAssistantCLI seam: the evaluator must reach the CLI
	// with the prefixed model, review-only shape and the Pi credential.
	setRunEvalCLIForTest(func(_ context.Context, req assistantCLIRequest, user domain.User, _ func(assistantStreamEvent)) (string, string, error) {
		calls <- cliCall{req: req, user: user}
		return `{"score":85,"summary":"pi 评测","dimensions":{"control":85,"latency":85,"tools":85,"tokens":85,"recovery":85,"delivery":85}}`, "", nil
	})
	t.Cleanup(func() { setRunEvalCLIForTest(nil) })

	started := requestJSONAuthed[domain.RunEvalRecord](t, h, piUser, http.MethodPost, "/api/runs/run-pi/eval", map[string]any{"model": "pi:eval-model"}, http.StatusAccepted)
	if started.Model != "pi:eval-model" {
		t.Fatalf("saved evaluator model = %q, want pi:eval-model", started.Model)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := requestJSONAuthed[domain.RunEvalRecord](t, h, piUser, http.MethodGet, "/api/runs/run-pi/eval", nil, http.StatusOK)
		if got.Status == domain.RunEvalSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	latest := requestJSONAuthed[domain.RunEvalRecord](t, h, piUser, http.MethodGet, "/api/runs/run-pi/eval", nil, http.StatusOK)
	if latest.Status != domain.RunEvalSucceeded {
		t.Fatalf("Pi evaluation did not succeed: %+v", latest)
	}
	select {
	case call := <-calls:
		if call.req.Model != "pi:eval-model" || !call.req.ReviewOnly {
			t.Fatalf("CLI request = %+v, want prefixed model with review-only", call.req)
		}
		if key, _ := assistantEngineCredential(call.user, "pi"); key != "pi-eval-key" {
			t.Fatalf("CLI user did not carry the Pi credential: %+v", call.user)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("evaluator never reached the fake CLI")
	}
}

func TestRunEvalExecutesNonCodeBuddyCLIWithOwnKey(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("fake CLI script needs a POSIX shell")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is required for the fake CLI")
	}
	t.Setenv("AGENTFLOW_SESSION_SECRET", "run-eval-session-secret")
	h, store, _ := newRunEvalHarness(t)
	if err := store.PutRun(domain.Run{ID: "run-claude", Status: "succeeded", WorkflowID: "wf-eval", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUser(domain.User{StaffID: 3105, LoginName: "claudeonly", ChineseName: "仅 Claude"}); err != nil {
		t.Fatal(err)
	}
	claudeUser, _ := store.UserByLoginName("claudeonly")
	if _, err := store.UpdateUserCredentials(claudeUser.ID, repo.UserCredentialUpdate{ClaudeCodeAPIKey: "claude-eval-key"}); err != nil {
		t.Fatal(err)
	}
	claudeUser, _ = store.User(claudeUser.ID)

	// A fake claude CLI standing in for the real binary: it records the bound
	// credential and --model, then returns a normal eval result.
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "bound-key")
	modelFile := filepath.Join(dir, "bound-model")
	bin := filepath.Join(dir, "claude")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s' "${ANTHROPIC_API_KEY:-}" > %q
model=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--model" ]; then model="$arg"; fi
  prev="$arg"
done
printf '%%s' "$model" > %q
printf '%%s\n' '{"type":"result","result":"{\"score\":88,\"summary\":\"claude 评测\",\"dimensions\":{\"control\":88,\"latency\":88,\"tools\":88,\"tokens\":88,\"recovery\":88,\"delivery\":88}}"}'
`, keyFile, modelFile)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CLAUDE_BIN", bin)

	started := requestJSONAuthed[domain.RunEvalRecord](t, h, claudeUser, http.MethodPost, "/api/runs/run-claude/eval", map[string]any{"model": "claude-code:eval-model"}, http.StatusAccepted)
	if started.Model != "claude-code:eval-model" {
		t.Fatalf("saved model = %q, want claude-code:eval-model", started.Model)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := requestJSONAuthed[domain.RunEvalRecord](t, h, claudeUser, http.MethodGet, "/api/runs/run-claude/eval", nil, http.StatusOK)
		if got.Status == domain.RunEvalSucceeded || got.Status == domain.RunEvalFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	latest := requestJSONAuthed[domain.RunEvalRecord](t, h, claudeUser, http.MethodGet, "/api/runs/run-claude/eval", nil, http.StatusOK)
	if latest.Status != domain.RunEvalSucceeded {
		t.Fatalf("eval did not succeed: %+v", latest)
	}
	if key, err := os.ReadFile(keyFile); err != nil || string(key) != "claude-eval-key" {
		t.Fatalf("fake CLI did not receive the Claude credential: key=%q err=%v", key, err)
	}
	if model, err := os.ReadFile(modelFile); err != nil || string(model) != "eval-model" {
		t.Fatalf("fake CLI did not receive the chosen model: model=%q err=%v", model, err)
	}
}

func TestRunEvalMissingNonCodeBuddyKeyNamesItsRuntime(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "run-eval-session-secret")
	h, store, _ := newRunEvalHarness(t)
	if err := store.PutRun(domain.Run{ID: "run-codex", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUser(domain.User{StaffID: 3104, LoginName: "nokey2", ChineseName: "无钥匙2"}); err != nil {
		t.Fatal(err)
	}
	user, _ := store.UserByLoginName("nokey2")
	refusal := requestJSONAuthed[map[string]string](t, h, user, http.MethodPost, "/api/runs/run-codex/eval", map[string]any{"model": "codex:gpt-test"}, http.StatusBadRequest)
	if !strings.Contains(refusal["error"], "Codex API Key") || strings.Contains(refusal["error"], "CodeBuddy") {
		t.Fatalf("refusal must name the selected runtime: %v", refusal)
	}
}

func TestRunEvalRejectsUnboundRunnerLoginWithoutPersonalKey(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "run-eval-session-secret")
	h, store, user := newRunEvalHarness(t)
	if err := store.PutRun(domain.Run{ID: "run-runner-login", Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	runner := domain.Runner{ID: "eval-login-runner", OwnerUserID: user.ID, Status: domain.RunnerStatusOnline,
		RunnerVersion: domain.RunnerVersionCLISettings,
		Runtimes:      []domain.RunnerRuntime{{Provider: "codex", Status: domain.RuntimeStatusOnline, AuthMode: domain.RuntimeAuthLocal}},
	}
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultRunner(user.ID, runner.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !scheduleHasPersonalCredential(store, user, "codex") {
		t.Fatal("fixture does not provide a valid runner login")
	}
	refusal := requestJSONAuthed[map[string]string](t, h, user, http.MethodPost, "/api/runs/run-runner-login/eval", map[string]any{"model": "codex:gpt-test"}, http.StatusBadRequest)
	if !strings.Contains(refusal["error"], "Codex API Key") {
		t.Fatalf("runner login must not authorize a control-plane evaluation: %v", refusal)
	}
	if _, exists := store.LatestRunEval("run-runner-login"); exists {
		t.Fatal("an evaluation was queued without a bound execution target or API Key")
	}
}

func TestRunEvalOtherCLIStillRequiresRunWriteAccess(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "run-eval-session-secret")
	h, store, user := newRunEvalHarness(t)
	if _, err := store.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{CodexAPIKey: "codex-eval-key"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUser(domain.User{StaffID: 3115, LoginName: "other-eval-owner", ChineseName: "其他所有者"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.UserByLoginName("other-eval-owner")
	if err := store.PutProject(domain.Project{ID: "private-eval-project", Name: "private", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []domain.Run{
		{ID: "private-project-eval", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "private-eval-project"}},
		{ID: "other-personal-eval", Kind: domain.RunKindWorkspace, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID}},
	} {
		if err := store.PutRun(run); err != nil {
			t.Fatal(err)
		}
		requestJSONAuthed[map[string]string](t, h, user, http.MethodPost, "/api/runs/"+run.ID+"/eval", map[string]any{"model": "codex:gpt-test"}, http.StatusForbidden)
		if _, exists := store.LatestRunEval(run.ID); exists {
			t.Fatalf("unauthorized evaluation was queued for %s", run.ID)
		}
	}
}

func TestRunEvalScoresAndAppliesNodePatch(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "run-eval-session-secret")
	h, store, user := newRunEvalHarness(t)
	if err := store.PutWorkflow(domain.WorkflowDefinition{ID: "wf-eval", Name: "评测SOP", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "fix", Name: "修复", Type: "agent", AgentID: "fixer", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAgent(domain.AgentDefinition{ID: "fixer", Name: "修复者", ProjectID: "platform", Prompt: "修 bug"}); err != nil {
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

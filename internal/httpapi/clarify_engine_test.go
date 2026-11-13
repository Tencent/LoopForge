package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Issue #228: clarification and run evals run on CodeBuddy only. A model of
// another CLI ("codex:…") must be refused up front, not silently swapped for
// the CodeBuddy default.
func TestClarifyModelEngineError(t *testing.T) {
	for _, id := range []string{"", "default", "glm-5.1", "public-model"} {
		if err := clarifyModelEngineError(id); err != nil {
			t.Fatalf("CodeBuddy model %q refused: %v", id, err)
		}
	}
	for _, id := range []string{"codex:gpt-5.5", "claude-code:sonnet"} {
		if err := clarifyModelEngineError(id); err != errClarifyNeedsCodeBuddy {
			t.Fatalf("model %q = %v, want errClarifyNeedsCodeBuddy", id, err)
		}
	}
}

func TestChatClarifyRefusesOtherCLIModel(t *testing.T) {
	called := false
	setClarifyExecForTest(func(context.Context, domain.Issue, chatClarifyRequest, func(string, any)) (any, string, map[string]any, error) {
		called = true
		return map[string]any{}, "{}", map[string]any{}, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })

	_, h := clarifyTestServer(t)
	body, _ := json.Marshal(map[string]any{"projectId": "platform", "message": "加个徽章", "model": "codex:gpt-5.5"})
	req := httptest.NewRequest(http.MethodPost, "/api/chat-clarify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "只支持 CodeBuddy") {
		t.Fatalf("error does not explain the CodeBuddy limit: %s", rec.Body.String())
	}
	if called {
		t.Fatal("clarification started with a Codex model")
	}
}

func TestRunEvalRefusesOtherCLIModel(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if _, err := f.s.UpdateUserCredentials(f.user.ID, repo.UserCredentialUpdate{CodeBuddyAPIKey: "eval-key"}); err != nil {
		t.Fatal(err)
	}
	done := domain.Run{ID: "eval-codex-run", Status: "succeeded", WorkflowID: "missing",
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}
	if err := f.s.PutRun(done); err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodPost, "/api/runs/eval-codex-run/eval", map[string]any{"model": "codex:gpt-5.5"}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "只支持 CodeBuddy") {
		t.Fatalf("status = %d body = %s, want 400 explaining the CodeBuddy limit", rec.Code, rec.Body.String())
	}
	if _, busy := f.s.ActiveRunEval(done.ID); busy {
		t.Fatal("a Codex model must not start a run eval")
	}
}

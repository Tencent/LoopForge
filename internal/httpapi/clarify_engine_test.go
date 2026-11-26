package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestChatClarifyRejectsInvalidModel(t *testing.T) {
	called := false
	setClarifyExecForTest(func(context.Context, domain.Issue, chatClarifyRequest, func(string, any)) (any, string, map[string]any, error) {
		called = true
		return map[string]any{}, "{}", map[string]any{}, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })

	_, h := clarifyTestServer(t)
	body, _ := json.Marshal(map[string]any{"projectId": "platform", "message": "加个徽章", "model": "codex:"})
	req := httptest.NewRequest(http.MethodPost, "/api/chat-clarify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "请选择有效的 CLI 模型") {
		t.Fatalf("error does not explain invalid model: %s", rec.Body.String())
	}
	if called {
		t.Fatal("clarification started with an invalid model")
	}
}

func TestRunEvalRequiresSelectedCLICredentials(t *testing.T) {
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
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Codex API Key") {
		t.Fatalf("status = %d body = %s, want 400 asking for the selected CLI credential", rec.Code, rec.Body.String())
	}
	if _, busy := f.s.ActiveRunEval(done.ID); busy {
		t.Fatal("a model without matching credentials must not start an eval")
	}
}

func TestRunEvalPreservesSelectedModelInChunkedRequest(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if _, err := f.s.UpdateUserCredentials(f.user.ID, repo.UserCredentialUpdate{CodexAPIKey: "eval-fixture"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "eval-chunked", Status: "succeeded", WorkflowID: "missing", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	selected := make(chan string, 1)
	setRunEvalExecForTest(func(_ context.Context, rec domain.RunEvalRecord, _ domain.Run, user domain.User, _ func(string, any)) (domain.RunEvalResult, error) {
		if user.ID == f.user.ID && user.CodexAPIKey == "eval-fixture" {
			selected <- rec.Model
		}
		return domain.RunEvalResult{}, nil
	})
	t.Cleanup(func() { setRunEvalExecForTest(nil) })
	req := httptest.NewRequest(http.MethodPost, "/api/runs/eval-chunked/eval", strings.NewReader(`{"model":"codex:private-model"}`))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, authedRequest(t, req, f.user))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case model := <-selected:
		if model != "codex:private-model" {
			t.Fatalf("job received the wrong explicit model: %q", model)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("eval job did not receive the requesting account's credential")
	}
}

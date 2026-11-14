package runner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestBatcherModelFailure(t *testing.T) {
	for _, tc := range []struct {
		name, line, status, wantStatus, wantClass string
	}{
		{"terminal error overrides exit zero", `{"type":"result","is_error":true,"result":"HTTP 429 Too Many Requests"}`, domain.RunnerTaskSuccess, domain.RunnerTaskFailed, "rate_limited"},
		{"nonzero exit keeps failure", `{"type":"result","is_error":true,"result":"HTTP 401 Unauthorized"}`, domain.RunnerTaskFailed, domain.RunnerTaskFailed, "auth"},
		{"codebuddy errors array", `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["401 invalid_api_key"]}`, domain.RunnerTaskSuccess, domain.RunnerTaskFailed, "auth"},
		{"success text is not failure", `{"type":"result","is_error":false,"result":"HTTP 429 Too Many Requests"}`, domain.RunnerTaskSuccess, domain.RunnerTaskSuccess, ""},
		{"unknown error keeps legacy status", `{"type":"result","is_error":true,"result":"unknown"}`, domain.RunnerTaskSuccess, domain.RunnerTaskSuccess, ""},
		{"tool failure is not model failure", `{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"HTTP 429 Too Many Requests"}]}}`, domain.RunnerTaskSuccess, domain.RunnerTaskSuccess, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batcher := newEventBatcher(domain.RunnerTask{Invocation: &domain.RunnerInvocation{Executor: domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}}}, nil)
			batcher.addLine("stdout", tc.line)
			got := batcher.withModelFailure(execResult{status: tc.status})
			if got.status != tc.wantStatus || got.modelErrorClass != tc.wantClass {
				t.Fatalf("result = %+v", got)
			}
		})
	}
}

func TestProcessModelFailureCompletion(t *testing.T) {
	server, store, _ := newDaemonTestServer(t)
	daemon, err := New(Config{ServerURL: server.URL, BootstrapToken: "boot-secret", StatePath: filepath.Join(t.TempDir(), "state.json"), DefaultWorkspaceRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := daemon.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{ID: "model-failure", Provider: "codebuddy", Status: domain.RunnerTaskQueued, MaxAttempts: 1,
		Invocation: &domain.RunnerInvocation{Executor: domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json"}}}
	if err := store.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimRunnerTask(daemon.runnerID(), []string{"codebuddy"}, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	result := daemon.executeProcess(ctx, claimed, []string{"/bin/sh", "-c", `printf '%s\n' '{"type":"result","is_error":true,"result":"HTTP 429 Too Many Requests; Retry-After: 42"}'`})
	if result.status != domain.RunnerTaskFailed || result.modelErrorClass != "rate_limited" || result.modelRetryAfterSeconds != 42 {
		t.Fatalf("result: %+v", result)
	}
	exit := result.exitCode
	if err := daemon.client.Complete(ctx, task.ID, CompleteRequest{Status: result.status, ExitCode: &exit, Result: result.result, Error: result.errMsg, ModelErrorClass: result.modelErrorClass, ModelRetryAfterSeconds: result.modelRetryAfterSeconds}); err != nil {
		t.Fatal(err)
	}
	stored, ok := store.RunnerTask(task.ID)
	if !ok || stored.Status != domain.RunnerTaskFailed || stored.ModelErrorClass != "rate_limited" || stored.ModelRetryAfterSeconds != 42 {
		t.Fatalf("stored: %+v", stored)
	}
}

package runtime

import (
	"context"
	"errors"
	"testing"

	"agentflow/internal/domain"
)

func TestClassifyModelFailure(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    ModelErrorClass
	}{
		{"Error: HTTP 429 Too Many Requests", ModelErrorRateLimited},
		{"rate_limit_error: rate limit exceeded", ModelErrorRateLimited},
		{"Error: HTTP 529 overloaded", ModelErrorOverloaded},
		{"status code: 503", ModelErrorOverloaded},
		{"Error: 401 Unauthorized", ModelErrorAuth},
		{"HTTP status 403", ModelErrorAuth},
		{"HTTP 429: insufficient_quota", ModelErrorQuotaExhausted},
		{"HTTP 400: context_length_exceeded", ModelErrorContextOverflow},
		{"HTTP 400 Bad Request", ModelErrorBadRequest},
		{"HTTP 504 Gateway Timeout", ModelErrorTimeout},
		{"request timed out", ModelErrorTimeout},
		{"exit status 429", ""},
		{"task number 429 failed", ""},
		{"HTTP 500 internal server error", ""},
		{"unknown problem", ""},
	} {
		t.Run(tc.message, func(t *testing.T) {
			if got := classifyModelFailure(tc.message); got != tc.want {
				t.Fatalf("classifyModelFailure(%q) = %q; want %q", tc.message, got, tc.want)
			}
		})
	}
}

func TestExecutorClassifiesTerminalModelFailure(t *testing.T) {
	processErr := errors.New("exit status 1")
	for _, tc := range []struct {
		name, kind, message string
		processErr          error
		want                ModelErrorClass
	}{
		{"claude rate limit", "claude-code", `{"type":"result","result":"HTTP 429 Too Many Requests","is_error":true}`, nil, ModelErrorRateLimited},
		{"codebuddy quota with nonzero exit", "codebuddy", `{"type":"result","result":"HTTP 429 insufficient_quota","is_error":true}`, processErr, ModelErrorQuotaExhausted},
		{"raw transcript is not error evidence", "claude-code", `{"type":"result","result":"tool failed","is_error":true}`, nil, ""},
		{"cancelled", "claude-code", `{"type":"result","result":"HTTP 429 Too Many Requests","is_error":true}`, context.Canceled, ""},
		{"success containing error text", "claude-code", `{"type":"result","result":"HTTP 429 Too Many Requests","is_error":false}`, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := &cvRProtocolSandbox{workspace: t.TempDir(), driver: "runner", err: tc.processErr,
				raw:   "assistant discussed HTTP 429 Too Many Requests",
				lines: []domain.TelemetryEvent{{Name: "process.stdout", Message: tc.message}}}
			_, err := NewExecutorRunner().Run(context.Background(), sb, "inv", domain.ExecutorDefinition{Kind: tc.kind, Binary: "cli", Protocol: "stream-json"},
				domain.AgentDefinition{ID: "a"}, domain.AgentAssetBundle{}, "prompt", InvocationOptions{}, nil)
			var modelErr *ModelError
			if errors.As(err, &modelErr) != (tc.want != "") {
				t.Fatalf("error = %v; want class %q", err, tc.want)
			}
			if modelErr != nil && modelErr.Class != tc.want {
				t.Fatalf("class = %q; want %q", modelErr.Class, tc.want)
			}
			if tc.processErr != nil && !errors.Is(err, tc.processErr) {
				t.Fatalf("original error lost: %v", err)
			}
		})
	}
}

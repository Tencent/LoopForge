package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// This opt-in test launches an installed Codex CLI against a local HTTP
// server. It does not require or send real provider credentials.
type codexHTTPTestSandbox struct {
	*localSandbox
	credentials RunCredentials
}

func (s *codexHTTPTestSandbox) Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	bound, err := bindCommand(s.localSandbox, s.Workspace(), command, s.credentials)
	if err != nil {
		return "", err
	}
	return s.localSandbox.Exec(ctx, bound, sink)
}

func TestRealCodexHTTPModelErrors(t *testing.T) {
	if os.Getenv("LOOPFORGE_REAL_CODEX_TEST") != "1" {
		t.Skip("set LOOPFORGE_REAL_CODEX_TEST=1 to run the installed Codex CLI")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("CLI: %s", strings.TrimSpace(string(version)))
	for _, tc := range []struct {
		name    string
		status  int
		message string
		class   ModelErrorClass
	}{
		{"auth", 401, "invalid_api_key", ModelErrorAuth},
		{"rate_limit", 429, "rate_limit_error", ModelErrorRateLimited},
		{"quota", 429, "insufficient_quota", ModelErrorQuotaExhausted},
		{"overload", 503, "overloaded_error", ModelErrorOverloaded},
		{"context", 400, "context_length_exceeded", ModelErrorContextOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"error":{"message":%q,"type":%q}}`, tc.message, tc.message)
			}))
			defer srv.Close()
			workspace := t.TempDir()
			sb := &codexHTTPTestSandbox{localSandbox: &localSandbox{id: "codex-live-protocol", workspace: workspace, environment: map[string]string{"NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost"}}, credentials: RunCredentials{Codex: ProviderCredential{APIKey: "local-test-key", BaseURL: srv.URL + "/v1"}}}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var terminal string
			ex := domain.ExecutorDefinition{Kind: "codex", Binary: binary, Protocol: "jsonl", Model: "gpt-test", Args: []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "-c", "model_providers.agentflow_user.request_max_retries=0", "-c", "model_providers.agentflow_user.stream_max_retries=0"}}
			_, err := NewExecutorRunner().Run(ctx, sb, "real-codex", ex, domain.AgentDefinition{ID: "probe"}, domain.AgentAssetBundle{}, "Reply hello.", InvocationOptions{}, func(ev domain.TelemetryEvent) {
				if ev.Name == "model.error" {
					terminal = ev.Message
				}
			})
			var failure *ModelError
			if !errors.As(err, &failure) || failure.Class != tc.class {
				t.Fatalf("HTTP %d: class=%v error=%v terminal=%q", tc.status, failure, err, terminal)
			}
			if requests.Load() == 0 {
				t.Fatal("CLI never sent an HTTP request")
			}
			if !strings.Contains(err.Error(), fmt.Sprint(tc.status)) && !strings.Contains(err.Error(), tc.message) && !(tc.class == ModelErrorQuotaExhausted && strings.Contains(err.Error(), "Quota exceeded")) {
				t.Fatalf("terminal diagnostic missing: %v", err)
			}
			t.Logf("HTTP=%d requests=%d class=%s retry-after=%s terminal-error=%v", tc.status, requests.Load(), failure.Class, failure.RetryAfter, failure.Err)
		})
	}
}

func TestRealCodexRateLimitThenSuccess(t *testing.T) {
	if os.Getenv("LOOPFORGE_REAL_CODEX_TEST") != "1" {
		t.Skip("set LOOPFORGE_REAL_CODEX_TEST=1 to run the installed Codex CLI")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate_limit_error","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_test","object":"response","status":"in_progress","output":[]}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_test","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
			`{"type":"response.content_part.added","item_id":"msg_test","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
			`{"type":"response.output_text.delta","item_id":"msg_test","output_index":0,"content_index":0,"delta":"hello"}`,
			`{"type":"response.output_text.done","item_id":"msg_test","output_index":0,"content_index":0,"text":"hello"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}}`,
			`{"type":"response.completed","response":{"id":"resp_test","object":"response","status":"completed","output":[{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer srv.Close()
	workspace := t.TempDir()
	sb := &codexHTTPTestSandbox{localSandbox: &localSandbox{id: "codex-success", workspace: workspace, environment: map[string]string{"NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost"}}, credentials: RunCredentials{Codex: ProviderCredential{APIKey: "local-test-key", BaseURL: srv.URL + "/v1"}}}
	ex := domain.ExecutorDefinition{Kind: "codex", Binary: binary, Protocol: "jsonl", Model: "gpt-test", Args: []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "-c", "model_providers.agentflow_user.request_max_retries=0", "-c", "model_providers.agentflow_user.stream_max_retries=0"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		output, err := NewExecutorRunner().Run(ctx, sb, fmt.Sprintf("codex-%d", i), ex, domain.AgentDefinition{ID: "probe"}, domain.AgentAssetBundle{}, "Reply hello.", InvocationOptions{}, nil)
		if i < 2 {
			var failure *ModelError
			if !errors.As(err, &failure) || failure.Class != ModelErrorRateLimited {
				t.Fatalf("invocation %d: %v", i+1, err)
			}
			t.Logf("invocation %d: %s", i+1, failure.Class)
			continue
		}
		if err != nil || !strings.Contains(output, "hello") {
			t.Fatalf("success: output=%q error=%v", output, err)
		}
		t.Logf("invocation %d: success %q", i+1, output)
	}
	if calls.Load() != 3 {
		t.Fatalf("HTTP requests=%d", calls.Load())
	}
}

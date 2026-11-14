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

// Opt in to exercising installed CLIs against a local HTTP server. CodeBuddy's
// third-party base URL is injected by the sandbox only for this test.
func TestRealClaudeAndCodeBuddyModelErrors(t *testing.T) {
	if os.Getenv("LOOPFORGE_REAL_CLI_TEST") != "1" {
		t.Skip("set LOOPFORGE_REAL_CLI_TEST=1 to run installed CLIs")
	}
	for _, provider := range []struct {
		binary, kind string
		args         []string
	}{
		{"claude", "claude-code", []string{"-p", "--output-format", "stream-json", "--permission-mode", "dontAsk"}},
		{"codebuddy", "codebuddy", []string{"-p", "--output-format", "stream-json", "--include-partial-messages", "-y", "--setting-sources", "none"}},
	} {
		t.Run(provider.kind, func(t *testing.T) {
			binary, err := exec.LookPath(provider.binary)
			if err != nil {
				t.Fatal(err)
			}
			version, err := exec.Command(binary, "--version").Output()
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("CLI: %s", strings.TrimSpace(string(version)))
			for _, tc := range []struct {
				name         string
				status       int
				message, typ string
				class        ModelErrorClass
			}{
				{"auth", 401, "invalid_api_key", "authentication_error", ModelErrorAuth},
				{"rate_limit", 429, "rate limit exceeded", "rate_limit_error", ModelErrorRateLimited},
				{"quota", 429, "insufficient_quota", "insufficient_quota", ModelErrorQuotaExhausted},
				{"overload", 503, "overloaded_error", "overloaded_error", ModelErrorOverloaded},
				{"context", 400, "context_length_exceeded", "invalid_request_error", ModelErrorContextOverflow},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var requests atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(tc.status)
						fmt.Fprintf(w, `{"type":"error","error":{"type":%q,"message":%q}}`, tc.typ, tc.message)
					}))
					defer srv.Close()
					workspace := t.TempDir()
					env := map[string]string{"NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost", "CLAUDE_CODE_MAX_RETRIES": "0", "CODEBUDDY_MAX_RETRIES": "0", "DISABLE_TELEMETRY": "1", "DISABLE_AUTOUPDATER": "1", "CODEBUDDY_BASE_URL": srv.URL + "/v1"}
					sb := &codexHTTPTestSandbox{localSandbox: &localSandbox{id: "cli-protocol", workspace: workspace, environment: env}, credentials: RunCredentials{ClaudeCode: ProviderCredential{APIKey: "local-test-key", BaseURL: srv.URL}, CodeBuddy: CodeBuddyCredential{APIKey: "local-test-key"}}}
					ex := domain.ExecutorDefinition{Kind: provider.kind, Binary: binary, Protocol: "stream-json", Model: "test-model", Args: provider.args}
					ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
					defer cancel()
					_, err := NewExecutorRunner().Run(ctx, sb, "real-cli", ex, domain.AgentDefinition{ID: "probe"}, domain.AgentAssetBundle{}, "Reply hello.", InvocationOptions{}, nil)
					var failure *ModelError
					if !errors.As(err, &failure) || failure.Class != tc.class {
						t.Fatalf("HTTP=%d requests=%d class=%v error=%v", tc.status, requests.Load(), failure, err)
					}
					if requests.Load() == 0 {
						t.Fatal("no HTTP request")
					}
					t.Logf("HTTP=%d requests=%d class=%s error=%s", tc.status, requests.Load(), failure.Class, failure.Err)
				})
			}
		})
	}
}

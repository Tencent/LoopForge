package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAssistantFailureClassifiesErrorBeforeStderrNoise(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"auth error with earlier rate-limit warning in stderr", fmt.Errorf("unexpected status 401 Unauthorized: invalid api key; stderr=WARN rate limit reached, retrying | bubblewrap: forbidden"), "AUTHENTICATION_FAILED"},
		{"stderr only fallback", fmt.Errorf("%w; stderr=HTTP 503 service unavailable", errors.New("exit status 1")), "UPSTREAM_UNAVAILABLE"},
		{"openai model name in 404", errors.New("unexpected status 404 Not Found: The model `nope` does not exist"), "MODEL_UNAVAILABLE"},
		{"quoted model name", errors.New(`The model "gpt-x" does not exist or you do not have access to it`), "MODEL_UNAVAILABLE"},
		{"plain exit", errors.New("exit status 1"), "CLI_EXIT_NONZERO"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAssistantFailure(context.Background(), tc.err).Code; got != tc.want {
				t.Fatalf("code=%s want %s", got, tc.want)
			}
		})
	}
}

func TestAssistantDiagnosticPrefersNewestStructuredErrorOverStderrText(t *testing.T) {
	g := domain.AssistantGeneration{ID: "structured-turn", SessionID: "s", Status: "running", StartedAt: time.Now().UTC()}
	d := newAssistantDiagnosticsCollector(g)
	a := d.begin("codex", "model", false, domain.User{})
	a.recordEvent(domain.AssistantDiagnosticEvent{Kind: "provider.error", Message: "rate limit reached, retrying", StatusCode: 429})
	a.recordEvent(domain.AssistantDiagnosticEvent{Kind: "provider.error", Message: "invalid api key", StatusCode: 401})
	err := fmt.Errorf("%w; stderr=WARN rate limit reached, retrying", errors.New("exit status 1"))
	if f := d.fail(context.Background(), err, domain.User{}); f.Code != "AUTHENTICATION_FAILED" {
		t.Fatalf("stderr retry warning outranked the final 401: %+v", f)
	}

	d = newAssistantDiagnosticsCollector(g)
	a = d.begin("codex", "model", false, domain.User{})
	a.recordEvent(domain.AssistantDiagnosticEvent{Kind: "provider.error", Message: "Not Found", StatusCode: 404})
	if f := d.fail(context.Background(), errors.New("exit status 1"), domain.User{}); f.Code != "MODEL_UNAVAILABLE" {
		t.Fatalf("provider 404 not mapped: %+v", f)
	}

	// A cause named by the error itself stays authoritative.
	d = newAssistantDiagnosticsCollector(g)
	a = d.begin("codex", "model", false, domain.User{})
	a.recordEvent(domain.AssistantDiagnosticEvent{Kind: "provider.error", Message: "rate limit", StatusCode: 429})
	if f := d.fail(context.Background(), errors.New("prompt is too long"), domain.User{}); f.Code != "CONTEXT_LIMIT_EXCEEDED" {
		t.Fatalf("explicit error overridden: %+v", f)
	}
}

func TestAssistantDiagnosticEndpointKeepsOnlySchemeAndHost(t *testing.T) {
	g := domain.AssistantGeneration{ID: "endpoint-turn", SessionID: "s", Status: "running", StartedAt: time.Now().UTC()}
	d := newAssistantDiagnosticsCollector(g)
	user := domain.User{CodexBaseURL: "https://user:pw@gateway.example.com/v1/path-secret-value/openai?key=q#f"}
	a := d.begin("codex", "model", false, user)
	a.configure("/nonexistent/codex", assistantCLIRequest{Model: "codex:model"})
	report := d.snapshot(g)
	if len(report.Attempts) != 1 || report.Attempts[0].Endpoint != "https://gateway.example.com" {
		t.Fatalf("endpoint kept path or credentials: %+v", report.Attempts)
	}
}

func TestAssistantDiagnosticCheckpointSkipsUnchangedEvidence(t *testing.T) {
	g := domain.AssistantGeneration{ID: "checkpoint-turn", SessionID: "s", Status: "running", StartedAt: time.Now().UTC()}
	d := newAssistantDiagnosticsCollector(g)
	if _, write := d.checkpointSnapshot(g); !write {
		t.Fatal("first checkpoint was skipped")
	}
	if _, write := d.checkpointSnapshot(g); write {
		t.Fatal("unchanged evidence rewritten")
	}
	d.platform("cli.preparing", "preparing", domain.User{})
	if _, write := d.checkpointSnapshot(g); !write {
		t.Fatal("changed evidence skipped")
	}
	d.mu.Lock()
	d.persistedAt = time.Now().Add(-assistantDiagnosticsRewriteInterval)
	d.mu.Unlock()
	if _, write := d.checkpointSnapshot(g); !write {
		t.Fatal("rewrite interval not honored")
	}
	g.Status = "failed"
	if _, write := d.checkpointSnapshot(g); !write {
		t.Fatal("final checkpoint skipped")
	}
}

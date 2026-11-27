package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAssistantFailureExplainsOpenCodeVariantIncompatibility(t *testing.T) {
	err := errors.New("exit status 1; stderr=ERROR: Unrecognized flag: --variant in command opencode run; private-path")
	failure := classifyAssistantFailure(context.Background(), err)
	if failure.Code != "OPENCODE_VARIANT_UNSUPPORTED" || !strings.Contains(failure.Message, "OpenCode") || !strings.Contains(failure.Suggestion, "1.x") || strings.Contains(failure.Message+failure.Suggestion, "private-path") {
		t.Fatalf("unexpected failure: %+v", failure)
	}

	unrelated := classifyAssistantFailure(context.Background(), errors.New("exit status 1; stderr=Unrecognized flag: --variant in command other run"))
	if unrelated.Code != "CLI_EXIT_NONZERO" {
		t.Fatalf("unrelated CLI was misclassified: %+v", unrelated)
	}
}

func TestAssistantOpenCodeVariantWarningDoesNotOverrideStructuredFailure(t *testing.T) {
	generation := domain.AssistantGeneration{ID: "opencode-warning", SessionID: "s", Status: "running", StartedAt: time.Now().UTC()}
	collector := newAssistantDiagnosticsCollector(generation)
	attempt := collector.begin("opencode", "model", false, domain.User{})
	attempt.recordEvent(domain.AssistantDiagnosticEvent{Kind: "provider.error", Message: "invalid api key", StatusCode: 401})
	err := errors.New("exit status 1; stderr=Unrecognized flag: --variant in command opencode run")
	if failure := collector.fail(context.Background(), err, domain.User{}); failure.Code != "AUTHENTICATION_FAILED" {
		t.Fatalf("old stderr warning hid structured failure: %+v", failure)
	}

	collector = newAssistantDiagnosticsCollector(generation)
	attempt = collector.begin("opencode", "model", false, domain.User{})
	attempt.recordEvent(domain.AssistantDiagnosticEvent{Kind: "provider.error", Message: "invalid api key", StatusCode: 401})
	err = errors.New("Unrecognized flag: --variant in command opencode run")
	if failure := collector.fail(context.Background(), err, domain.User{}); failure.Code != "OPENCODE_VARIANT_UNSUPPORTED" {
		t.Fatalf("explicit CLI failure was overridden: %+v", failure)
	}
}

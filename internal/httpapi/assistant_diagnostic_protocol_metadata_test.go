package httpapi

import (
	"encoding/json"
	"testing"

	"agentflow/internal/domain"
)

func protocolMetadataFrom(t *testing.T, line string, user domain.User) domain.AssistantDiagnosticEvent {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatalf("decode %s: %v", line, err)
	}
	var e domain.AssistantDiagnosticEvent
	inspectProtocolMetadata(&e, raw, 0, user)
	return e
}

func TestInspectProtocolMetadataReadsTopLevelFields(t *testing.T) {
	e := protocolMetadataFrom(t, `{"type":"turn.failed","requestId":"req-1","trace_id":"tr-1","errorCode":"rate_limit","errorPhase":"stream","statusCode":429,"willRetry":true,"message":"slow down"}`, domain.User{})
	if e.Kind != "turn.failed" || e.RequestID != "req-1" || e.TraceID != "tr-1" || e.Code != "rate_limit" || e.Phase != "stream" {
		t.Fatalf("unexpected identifiers: %+v", e)
	}
	if e.StatusCode != 429 {
		t.Fatalf("status code = %d, want 429", e.StatusCode)
	}
	if e.Retryable == nil || !*e.Retryable {
		t.Fatalf("retryable = %v, want true", e.Retryable)
	}
	if e.Message != "slow down" {
		t.Fatalf("message = %q, want %q", e.Message, "slow down")
	}
}

func TestInspectProtocolMetadataNestedErrorSetsProviderKind(t *testing.T) {
	e := protocolMetadataFrom(t, `{"type":"result","error":{"message":"upstream exploded","code":"E42","status_code":502}}`, domain.User{})
	if e.Kind != "provider.error" {
		t.Fatalf("kind = %q, want provider.error", e.Kind)
	}
	if e.Code != "E42" || e.StatusCode != 502 || e.Message != "upstream exploded" {
		t.Fatalf("unexpected nested metadata: %+v", e)
	}
}

func TestInspectProtocolMetadataCodeOnlyFillsMissingErrorCode(t *testing.T) {
	e := protocolMetadataFrom(t, `{"errorCode":"primary","code":"fallback"}`, domain.User{})
	if e.Code != "primary" {
		t.Fatalf("code = %q, want primary", e.Code)
	}
	e = protocolMetadataFrom(t, `{"code":"fallback"}`, domain.User{})
	if e.Code != "fallback" {
		t.Fatalf("code = %q, want fallback", e.Code)
	}
}

func TestInspectProtocolMetadataIgnoresOutOfRangeStatusAndMessageWithoutKind(t *testing.T) {
	e := protocolMetadataFrom(t, `{"type":"assistant","statusCode":42,"httpStatusCode":700,"message":"plain assistant text"}`, domain.User{})
	if e.Kind != "" || e.StatusCode != 0 || e.Message != "" {
		t.Fatalf("expected no metadata, got %+v", e)
	}
}

func TestInspectProtocolMetadataStopsBelowDepthLimit(t *testing.T) {
	// Levels 0..5 are inspected; the requestId at level 6 is out of reach.
	reachable := `{"params":{"params":{"params":{"params":{"params":{"requestId":"deep-5"}}}}}}`
	if e := protocolMetadataFrom(t, reachable, domain.User{}); e.RequestID != "deep-5" {
		t.Fatalf("request id at depth 5 = %q, want deep-5", e.RequestID)
	}
	tooDeep := `{"params":{"params":{"params":{"params":{"params":{"params":{"requestId":"deep-6"}}}}}}}`
	if e := protocolMetadataFrom(t, tooDeep, domain.User{}); e.RequestID != "" {
		t.Fatalf("request id beyond depth limit = %q, want empty", e.RequestID)
	}
}

func TestInspectProtocolMetadataRedactsUserSecrets(t *testing.T) {
	user := domain.User{CodexAPIKey: "user-secret-value"}
	e := protocolMetadataFrom(t, `{"type":"error","message":"bad key user-secret-value"}`, user)
	if e.Message != "bad key <redacted>" {
		t.Fatalf("message = %q, want secret redacted", e.Message)
	}
}

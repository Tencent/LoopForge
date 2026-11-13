package httpapi

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// TestInspectProtocolMetadataErrorLikeKinds pins each keyword that makes a
// type/method name count as a diagnostic kind, and that method is consulted
// as well as type.
func TestInspectProtocolMetadataErrorLikeKinds(t *testing.T) {
	for _, tc := range []struct {
		line, want string
	}{
		{`{"type":"stream_error"}`, "stream_error"},
		{`{"type":"turn.failed"}`, "turn.failed"},
		{`{"type":"request.retry"}`, "request.retry"},
		{`{"type":"rate_limit_warning"}`, "rate_limit_warning"},
		{`{"method":"codex/event/stream_error"}`, "codex/event/stream_error"},
		{`{"type":"assistant","method":"turn/started"}`, ""},
	} {
		if e := protocolMetadataFrom(t, tc.line, domain.User{}); e.Kind != tc.want {
			t.Errorf("%s: kind = %q, want %q", tc.line, e.Kind, tc.want)
		}
	}
}

// TestInspectProtocolMetadataFieldAliases checks every accepted spelling of
// the request ID, trace ID, error code, HTTP status and retry flag.
func TestInspectProtocolMetadataFieldAliases(t *testing.T) {
	e := protocolMetadataFrom(t, `{"request_id":"req-snake","traceId":"tr-camel","providerErrorCode":"quota","httpStatusCode":503,"retryable":false}`, domain.User{})
	if e.RequestID != "req-snake" || e.TraceID != "tr-camel" || e.Code != "quota" || e.StatusCode != 503 {
		t.Fatalf("aliases not read: %+v", e)
	}
	if e.Retryable == nil || *e.Retryable {
		t.Fatalf("retryable = %v, want explicit false", e.Retryable)
	}

	e = protocolMetadataFrom(t, `{"providerErrorCode":"quota","code":"generic"}`, domain.User{})
	if e.Code != "quota" {
		t.Fatalf("code = %q, providerErrorCode must win over code", e.Code)
	}
}

// TestInspectProtocolMetadataStatusCodeBounds pins the inclusive 100..599 range.
func TestInspectProtocolMetadataStatusCodeBounds(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
	}{
		{`{"statusCode":99}`, 0},
		{`{"statusCode":100}`, 100},
		{`{"statusCode":599}`, 599},
		{`{"statusCode":600}`, 0},
		{`{"statusCode":"500"}`, 0},
	} {
		if e := protocolMetadataFrom(t, tc.line, domain.User{}); e.StatusCode != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.line, e.StatusCode, tc.want)
		}
	}
}

// TestInspectProtocolMetadataWalksEveryKnownContainer makes sure each nested
// container key is descended into, and that unknown keys are not.
func TestInspectProtocolMetadataWalksEveryKnownContainer(t *testing.T) {
	for _, key := range []string{"payload", "params", "data", "error", "attribution", "turn", "codexErrorInfo", "httpConnectionFailed", "responseStreamDisconnected", "responseStreamConnectionFailed"} {
		t.Run(key, func(t *testing.T) {
			e := protocolMetadataFrom(t, `{"`+key+`":{"requestId":"req-`+key+`","httpStatusCode":502}}`, domain.User{})
			if e.RequestID != "req-"+key || e.StatusCode != 502 {
				t.Fatalf("nested %s not inspected: %+v", key, e)
			}
		})
	}
	if e := protocolMetadataFrom(t, `{"result":{"requestId":"req-hidden"}}`, domain.User{}); e.RequestID != "" {
		t.Fatalf("unknown container was inspected: %+v", e)
	}
}

// TestInspectProtocolMetadataMessageSources covers the three message keys and
// that a plain string "error" field becomes the message once a kind is known.
func TestInspectProtocolMetadataMessageSources(t *testing.T) {
	for _, tc := range []struct {
		line, want string
	}{
		{`{"type":"error","message":"from message"}`, "from message"},
		{`{"type":"error","providerErrorMessage":"from provider"}`, "from provider"},
		{`{"type":"error","error":"from error string"}`, "from error string"},
		{`{"type":"error","message":"first","providerErrorMessage":"second"}`, "second"},
	} {
		if e := protocolMetadataFrom(t, tc.line, domain.User{}); e.Message != tc.want {
			t.Errorf("%s: message = %q, want %q", tc.line, e.Message, tc.want)
		}
	}
}

// TestInspectProtocolMetadataTruncatesEachField pins the per-field rune limits:
// 120 for the kind, 160 for IDs, codes and phase, 1024 for the message.
func TestInspectProtocolMetadataTruncatesEachField(t *testing.T) {
	kind := strings.Repeat("k", 115) + "_error"
	id := strings.Repeat("r", 170)
	message := strings.Repeat("m ", 600)
	e := protocolMetadataFrom(t, `{"type":"`+kind+`","requestId":"`+id+`","traceId":"`+id+`","errorCode":"`+id+`","errorPhase":"`+id+`","message":"`+message+`"}`, domain.User{})
	if want := truncateRunes(kind, 120); e.Kind != want || !strings.HasSuffix(e.Kind, "…") {
		t.Fatalf("kind = %q, want %q", e.Kind, want)
	}
	wantID := id[:160] + "…"
	for name, got := range map[string]string{"requestId": e.RequestID, "traceId": e.TraceID, "code": e.Code, "phase": e.Phase} {
		if got != wantID {
			t.Errorf("%s = %q (len %d), want 160 runes plus ellipsis", name, got, len(got))
		}
	}
	if want := truncateRunes(message, 1024); e.Message != want || len([]rune(e.Message)) != 1025 {
		t.Fatalf("message has %d runes, want 1024 plus ellipsis", len([]rune(e.Message)))
	}

	short := strings.Repeat("k", 114) + "_error"
	if e := protocolMetadataFrom(t, `{"type":"`+short+`"}`, domain.User{}); e.Kind != short {
		t.Fatalf("kind of exactly 120 runes was clipped: %q", e.Kind)
	}
}

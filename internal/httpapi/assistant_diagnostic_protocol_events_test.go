package httpapi

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func protocolAttemptEvents(t *testing.T, lines ...string) ([]domain.AssistantDiagnosticEvent, bool) {
	t.Helper()
	d := newAssistantDiagnosticsCollector(domain.AssistantGeneration{ID: "protocol-events", Status: "running"})
	a := d.begin("codex", "model", false, domain.User{})
	for _, line := range lines {
		a.protocol(line)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.report.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(d.report.Attempts))
	}
	return append([]domain.AssistantDiagnosticEvent(nil), d.report.Attempts[0].Events...), d.report.Truncated
}

// protocol must keep the metadata inspectProtocolMetadata extracted: IDs or a
// status code without an error-like kind are recorded as provider.metadata.
func TestAssistantDiagnosticProtocolRecordsMetadataOnlyLines(t *testing.T) {
	events, _ := protocolAttemptEvents(t,
		`{"type":"assistant","requestId":"req-only","traceId":"tr-only"}`,
		`{"method":"turn/completed","params":{"statusCode":503}}`,
	)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want 2", events)
	}
	if e := events[0]; e.Kind != "provider.metadata" || e.RequestID != "req-only" || e.TraceID != "tr-only" || e.Source != "cli" {
		t.Fatalf("id-only event = %+v", e)
	}
	if e := events[1]; e.Kind != "provider.metadata" || e.StatusCode != 503 {
		t.Fatalf("status-only event = %+v", e)
	}
}

// Nested metadata found by the extracted walker must reach the recorded event.
func TestAssistantDiagnosticProtocolRecordsNestedErrorKind(t *testing.T) {
	events, _ := protocolAttemptEvents(t, `{"type":"result","payload":{"error":{"requestId":"req-nested","message":"upstream down"}}}`)
	if len(events) != 1 {
		t.Fatalf("events = %+v, want 1", events)
	}
	if e := events[0]; e.Kind != "provider.error" || e.RequestID != "req-nested" || e.Message != "upstream down" {
		t.Fatalf("nested event = %+v", e)
	}
}

func TestAssistantDiagnosticProtocolSkipsLinesWithoutMetadata(t *testing.T) {
	events, truncated := protocolAttemptEvents(t,
		`{"type":"assistant","message":"plain assistant text","statusCode":42}`,
		`not json`,
		`["array"]`,
	)
	if len(events) != 0 || truncated {
		t.Fatalf("events = %+v truncated = %v, want none", events, truncated)
	}
}

func TestAssistantDiagnosticProtocolMarksOversizedLineTruncated(t *testing.T) {
	huge := `{"type":"error","requestId":"req-huge","message":"` + strings.Repeat("x", 256<<10) + `"}`
	events, truncated := protocolAttemptEvents(t, huge)
	if len(events) != 0 || !truncated {
		t.Fatalf("events = %d truncated = %v, want none and truncated", len(events), truncated)
	}
}

func TestAssistantDiagnosticProtocolNilAttemptIsNoop(t *testing.T) {
	var a *assistantDiagnosticAttempt
	a.protocol(`{"type":"error","requestId":"req"}`)
}

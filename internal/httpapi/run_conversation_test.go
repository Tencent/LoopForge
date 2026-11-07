package httpapi

import (
	"agentflow/internal/domain"
	"testing"
)

func TestExpandedConversationPreservesToolAndArtifactIdentity(t *testing.T) {
	tool := domain.TelemetryEvent{Category: "tool", Tool: &domain.ToolObservation{Name: "Read", CallID: "call-1"}, Attributes: map[string]any{"private": "hidden"}}
	got := expandedTelemetryEvent(tool)
	if got.Tool == nil || got.Tool.Name != "Read" || got.Attributes != nil {
		t.Fatalf("tool identity lost or raw attributes exposed: %#v", got)
	}
	artifact := domain.TelemetryEvent{Category: "artifact", Name: "artifact.published", Attributes: map[string]any{"artifactId": "file-1", "path": "result.md", "size": 42, "private": "hidden"}}
	got = expandedTelemetryEvent(artifact)
	if got.Attributes["artifactId"] != "file-1" || got.Attributes["path"] != "result.md" || len(got.Attributes) != 3 {
		t.Fatalf("artifact linkage lost or extra attributes exposed: %#v", got.Attributes)
	}
	if slimTelemetryEvent(tool).Tool != nil || slimTelemetryEvent(artifact).Attributes != nil {
		t.Fatal("compact telemetry contract changed")
	}
}

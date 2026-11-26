package httpapi

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeArtifactImageAcceptsDataURL(t *testing.T) {
	raw := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	got, err := decodeArtifactImage("data:image/png;base64," + base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if sniffImageFormat(got) != "png" {
		t.Fatalf("format = %q", sniffImageFormat(got))
	}
	if !looksLikeLocalPath("/tmp/demo.png") || looksLikeLocalPath("<html></html>") {
		t.Fatal("local path detection drifted")
	}
}

func TestPublishToolAppearsOnlyForSpaceSession(t *testing.T) {
	h := &agentToolGateway{}
	without := h.buildCatalog(context.Background(), &AgentToolSession{Mode: AgentToolAssistant})
	for _, tool := range without {
		if tool.Name == "workbench_publish_artifact" {
			t.Fatal("publish tool must stay hidden outside a conversation")
		}
	}
	with := h.buildCatalog(context.Background(), &AgentToolSession{Mode: AgentToolAssistant, SpaceID: "room-1"})
	found := false
	for _, tool := range with {
		if tool.Name == "workbench_publish_artifact" {
			found = true
		}
	}
	if !found {
		t.Fatal("space session is missing workbench_publish_artifact")
	}
	if !strings.Contains(spaceDeliverableSuffix(&AgentToolSession{SpaceID: "room-1"}), "workbench_publish_artifact") {
		t.Fatal("space sessions must be told to publish deliverables")
	}
	if spaceDeliverableSuffix(&AgentToolSession{}) != "" {
		t.Fatal("personal sessions should not get the space deliverable rule")
	}
}

// One deliverable tool: in a workspace it takes a file path (any
// conversation); content fields only exist where a space sidebar does.
func TestPublishArtifactSchemaFollowsModeAndSpace(t *testing.T) {
	schema := func(s *AgentToolSession, workspace bool) string {
		return string(publishArtifactDescriptor(s, workspace).InputSchema)
	}
	plain := &AgentToolSession{Mode: AgentToolAssistant}
	space := &AgentToolSession{Mode: AgentToolAssistant, SpaceID: "room-1"}
	if got := schema(plain, true); !strings.Contains(got, `"path"`) || strings.Contains(got, `"body"`) {
		t.Fatalf("workspace schema outside a space: %s", got)
	}
	if got := schema(space, true); !strings.Contains(got, `"path"`) || !strings.Contains(got, `"body"`) {
		t.Fatalf("workspace schema in a space: %s", got)
	}
	if got := schema(space, false); strings.Contains(got, `"path"`) || !strings.Contains(got, `"imageBase64"`) {
		t.Fatalf("platform schema in a space: %s", got)
	}
	names := map[string]bool{}
	for _, tool := range workspaceIntegrationCatalog(&AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", UserID: 1, ChatSessionID: "c", ChatOwnerID: 1}) {
		names[tool.Name] = true
	}
	if !names["workbench_publish_artifact"] || names["workbench_workspace_attach"] {
		t.Fatalf("workspace catalog = %v", names)
	}
	for _, noisy := range []string{"runs_logs_download", "issues_chat_clarify", "issues_chat_clarify_stream", "clarification_chat"} {
		if assistantPlatformToolAllowed(assistantExecMode{AutoWrite: true}, noisy) {
			t.Errorf("%s should not be offered to agents", noisy)
		}
	}
}

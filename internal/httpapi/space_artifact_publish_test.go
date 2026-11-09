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

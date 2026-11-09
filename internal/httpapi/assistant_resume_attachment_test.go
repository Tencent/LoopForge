package httpapi

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestAssistantIncrementalPromptKeepsOnlyCurrentAttachmentNote(t *testing.T) {
	messages := []assistantMessage{
		{Role: "user", Content: "old question", Attachments: []domain.AssistantAttachment{{ID: "old-file", Name: "old.png", MimeType: "image/png"}}},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "inspect this image", Attachments: []domain.AssistantAttachment{{ID: "new-file", Name: "new.png", MimeType: "image/png", Size: 12}}},
	}
	prompt := assistantIncrementalPrompt(assistantRequest{Messages: messages}, assistantNativeCheckpoint{MessageHashes: assistantMessageHashes(messages[:2])})
	for _, want := range []string{"inspect this image", "new-file", "new.png", "workbench_attachment_read"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("incremental prompt missing %q: %s", want, prompt)
		}
	}
	for _, old := range []string{"old question", "old-file", "old.png"} {
		if strings.Contains(prompt, old) {
			t.Fatalf("incremental prompt reintroduced %q", old)
		}
	}

	messages[2].Attachments = nil
	prompt = assistantIncrementalPrompt(assistantRequest{Messages: messages}, assistantNativeCheckpoint{MessageHashes: assistantMessageHashes(messages[:2])})
	if strings.Contains(prompt, "old-file") || strings.Contains(prompt, "本轮用户附件") {
		t.Fatalf("attachment-free turn reused previous files: %s", prompt)
	}
}

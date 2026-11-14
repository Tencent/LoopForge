package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

func TestTranscriptAttachmentOwner(t *testing.T) {
	attachments := func(ids ...string) []domain.AssistantAttachment {
		out := make([]domain.AssistantAttachment, 0, len(ids))
		for _, id := range ids {
			out = append(out, domain.AssistantAttachment{ID: id})
		}
		return out
	}
	session := domain.AssistantSession{
		UserID: 1,
		Messages: []domain.AssistantMessage{
			{Role: "user", AuthorID: 7, Attachments: attachments("a", "b")},
			{Role: "user", Attachments: attachments("c")},
			{Role: "assistant", Content: "no files"},
			{Role: "user", AuthorID: 9, Attachments: attachments("b")},
		},
	}
	cases := []struct {
		name  string
		id    string
		owner int64
		found bool
	}{
		{name: "message author", id: "a", owner: 7, found: true},
		{name: "authorless message falls back to session owner", id: "c", owner: 1, found: true},
		{name: "last listing wins", id: "b", owner: 9, found: true},
		{name: "not in transcript", id: "missing", owner: 0, found: false},
		{name: "empty id", id: "", owner: 0, found: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, found := transcriptAttachmentOwner(session, tc.id)
			if owner != tc.owner || found != tc.found {
				t.Fatalf("transcriptAttachmentOwner(%q) = (%d, %v), want (%d, %v)", tc.id, owner, found, tc.owner, tc.found)
			}
		})
	}
	if owner, found := transcriptAttachmentOwner(domain.AssistantSession{UserID: 3}, "a"); owner != 0 || found {
		t.Fatalf("empty transcript = (%d, %v), want (0, false)", owner, found)
	}
}

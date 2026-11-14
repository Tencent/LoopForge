package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

// sharedAttachmentTurn appends a user turn by authorID carrying attachment
// to the owner's conversation and returns the reloaded session.
func sharedAttachmentTurn(t *testing.T, h *assistantHandlers, owner domain.User, s domain.AssistantSession, authorID int64, attachment domain.AssistantAttachment) domain.AssistantSession {
	t.Helper()
	s.Messages = append(s.Messages,
		domain.AssistantMessage{Role: "assistant", Content: "旧回复"},
		domain.AssistantMessage{Role: "user", Content: "带附件的问题", AuthorID: authorID, Attachments: []domain.AssistantAttachment{attachment}},
	)
	if err := h.repo.PutAssistantSession(owner.ID, s); err != nil {
		t.Fatal(err)
	}
	s, _ = h.repo.AssistantSession(owner.ID, s.ID)
	return s
}

func postEditedTurn(t *testing.T, user domain.User, sessionID string, index int, updatedAt any, handler func(*gin.Context)) (int, string) {
	t.Helper()
	body, _ := json.Marshal(gin.H{"messageIndex": index, "content": "改过的问题", "updatedAt": updatedAt})
	c, w := workbenchContext(user, "POST", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: sessionID}}
	handler(c)
	return w.Code, w.Body.String()
}

func requireEditorOwnsCopy(t *testing.T, editor domain.User, original domain.AssistantAttachment, got []domain.AssistantAttachment) {
	t.Helper()
	if len(got) != 1 || got[0].ID == "" || got[0].ID == original.ID {
		t.Fatalf("attachment was not copied for the editor: original=%s got=%+v", original.ID, got)
	}
	record, file, err := openWorkbenchAttachment(editor.ID, got[0].ID)
	if err != nil {
		t.Fatalf("editor cannot open copied attachment: %v", err)
	}
	file.Close()
	if record.ProjectID != "workbench" {
		t.Fatalf("copied attachment project = %q", record.ProjectID)
	}
}

// forkSession must hand the forking member a private copy of attachments
// that another member (or, for legacy turns, the session owner) uploaded.
func TestForkSessionCopiesForeignAttachmentsToForkingUser(t *testing.T) {
	for _, tc := range []struct {
		name         string
		legacyAuthor bool
	}{{"author recorded", false}, {"legacy turn falls back to owner", true}} {
		t.Run(tc.name, func(t *testing.T) {
			h, a, b, _, s := sharedWorkbenchFixture(t)
			saved, err := saveWorkbenchImage(a.ID, "spec.txt", "text/plain", []byte("spec"), s.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			author := a.ID
			if tc.legacyAuthor {
				author = 0
			}
			s = sharedAttachmentTurn(t, h, a, s, author, saved)
			index := len(s.Messages) - 1

			code, body := postEditedTurn(t, b, s.ID, index, s.UpdatedAt, h.forkSession)
			if code != 201 {
				t.Fatal(code, body)
			}
			var branch domain.AssistantSession
			if err := json.Unmarshal([]byte(body), &branch); err != nil {
				t.Fatal(err)
			}
			if branch.UserID != b.ID || len(branch.Messages) != index+1 {
				t.Fatalf("unexpected branch: %+v", branch)
			}
			requireEditorOwnsCopy(t, b, saved, branch.Messages[index].Attachments)

			original, _ := h.repo.AssistantSession(a.ID, s.ID)
			if got := original.Messages[index].Attachments; len(got) != 1 || got[0].ID != saved.ID {
				t.Fatalf("original turn attachments changed: %+v", got)
			}
		})
	}
}

// editSession rewrites the same conversation: another member's edit must
// reference a copy owned by that member, while the author editing their own
// turn keeps the original attachment.
func TestEditSessionClaimsAttachmentsOnlyForOtherAuthors(t *testing.T) {
	h, a, b, _, s := sharedWorkbenchFixture(t)
	saved, err := saveWorkbenchImage(a.ID, "spec.txt", "text/plain", []byte("spec"), s.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	s = sharedAttachmentTurn(t, h, a, s, a.ID, saved)
	index := len(s.Messages) - 1

	if code, body := postEditedTurn(t, a, s.ID, index, s.UpdatedAt, h.editSession); code != 200 {
		t.Fatal(code, body)
	}
	own, _ := h.repo.AssistantSession(a.ID, s.ID)
	if got := own.Messages[index].Attachments; len(got) != 1 || got[0].ID != saved.ID || own.Messages[index].AuthorID != a.ID {
		t.Fatalf("author edit must keep its own attachment: %+v", own.Messages[index])
	}

	if code, body := postEditedTurn(t, b, s.ID, index, own.UpdatedAt, h.editSession); code != 200 {
		t.Fatal(code, body)
	}
	edited, _ := h.repo.AssistantSession(a.ID, s.ID)
	if edited.Messages[index].AuthorID != b.ID {
		t.Fatalf("edit lost attribution: %+v", edited.Messages[index])
	}
	requireEditorOwnsCopy(t, b, saved, edited.Messages[index].Attachments)
}

// When a foreign attachment can no longer be copied, both handlers reject
// the request and leave the stored conversation untouched.
func TestForkAndEditRejectUncopyableAttachments(t *testing.T) {
	h, a, b, _, s := sharedWorkbenchFixture(t)
	missing := domain.AssistantAttachment{ID: "0123456789abcdef0123456789abcdef", Name: "gone.txt", MimeType: "text/plain"}
	s = sharedAttachmentTurn(t, h, a, s, a.ID, missing)
	index := len(s.Messages) - 1
	before := h.repo.ListAssistantSessions(b.ID)

	for _, tc := range []struct {
		name    string
		handler func(*gin.Context)
		message string
	}{
		{"fork", h.forkSession, "无法复制原消息附件"},
		{"edit", h.editSession, "无法复制消息附件"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postEditedTurn(t, b, s.ID, index, s.UpdatedAt, tc.handler)
			if code != 400 || !strings.Contains(body, tc.message) {
				t.Fatalf("status=%d body=%s", code, body)
			}
			stored, _ := h.repo.AssistantSession(a.ID, s.ID)
			if len(stored.Messages) != len(s.Messages) || stored.Messages[index].Content != "带附件的问题" || stored.Messages[index].AuthorID != a.ID {
				t.Fatalf("conversation changed after rejection: %+v", stored.Messages[index])
			}
		})
	}
	if after := h.repo.ListAssistantSessions(b.ID); len(after) != len(before) {
		t.Fatalf("rejected fork created a branch: before=%d after=%d", len(before), len(after))
	}
}

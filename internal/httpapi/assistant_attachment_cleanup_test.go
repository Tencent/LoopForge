package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func writeTestAttachment(t *testing.T, owner int64, id string) {
	t.Helper()
	dir := workbenchAttachmentDir(owner)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{id + ".bin", id + ".json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func testAttachmentExists(owner int64, id string) bool {
	_, bin := os.Stat(filepath.Join(workbenchAttachmentDir(owner), id+".bin"))
	_, meta := os.Stat(filepath.Join(workbenchAttachmentDir(owner), id+".json"))
	return bin == nil || meta == nil
}

// Deleting a conversation removes the files its messages listed from their
// posters' attachment directories, except those something else still lists.
func TestDeletingConversationRemovesItsAttachments(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	h, owner, _ := scheduleSessionFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "sess-peer", CodeBuddyAPIKey: "peer"}); err != nil {
		t.Fatal(err)
	}
	peer, _ := h.repo.UserByLoginName("sess-peer")
	own, forked, inRoom, peers := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	unrelated := strings.Repeat("e", 32)
	for _, id := range []string{own, forked, inRoom, unrelated} {
		writeTestAttachment(t, owner.ID, id)
	}
	writeTestAttachment(t, peer.ID, peers)
	att := func(id string) []domain.AssistantAttachment {
		return []domain.AssistantAttachment{{ID: id, Name: id[:1] + ".png", MimeType: "image/png"}}
	}
	now := time.Now().UTC()
	// A path-like id is never treated as a file name.
	traversal := domain.AssistantAttachment{ID: "../../" + strings.Repeat("f", 26), Name: "x"}
	session := domain.AssistantSession{ID: "chat-with-files", ProjectID: "sess-project", Title: "files", Messages: []domain.AssistantMessage{
		{Role: "user", Content: "看图", Attachments: append(att(own), traversal), CreatedAt: now},
		{Role: "user", Content: "我的截图", AuthorID: peer.ID, Attachments: att(peers), CreatedAt: now},
		{Role: "assistant", Content: `{"message":"好","drafts":[]}`, Attachments: append(att(forked), att(inRoom)...), CreatedAt: now},
	}}
	if err := h.repo.PutAssistantSession(owner.ID, session); err != nil {
		t.Fatal(err)
	}
	fork := domain.AssistantSession{ID: "chat-fork", ProjectID: "sess-project", Title: "fork", Messages: []domain.AssistantMessage{
		{Role: "assistant", Content: `{"message":"好","drafts":[]}`, Attachments: att(forked), CreatedAt: now},
	}}
	if err := h.repo.PutAssistantSession(owner.ID, fork); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutMessage(domain.ChatMessage{ID: "room-msg", RoomID: "room-1", UserID: owner.ID, Kind: domain.ChatKindText, Body: "图", Attachments: att(inRoom)}); err != nil {
		t.Fatal(err)
	}

	if err := h.deleteConversation(owner.ID, session.ID); err != nil {
		t.Fatal(err)
	}
	if testAttachmentExists(owner.ID, own) {
		t.Error("the deleted conversation's own file was kept")
	}
	if testAttachmentExists(peer.ID, peers) {
		t.Error("a participant's file posted in the deleted conversation was kept")
	}
	if !testAttachmentExists(owner.ID, forked) {
		t.Error("a file a fork still lists was removed")
	}
	if !testAttachmentExists(owner.ID, inRoom) {
		t.Error("a file a room message still lists was removed")
	}
	if !testAttachmentExists(owner.ID, unrelated) {
		t.Error("a file the conversation never listed was removed")
	}

	if err := h.deleteConversation(owner.ID, fork.ID); err != nil {
		t.Fatal(err)
	}
	if testAttachmentExists(owner.ID, forked) {
		t.Error("the file stayed after the last conversation listing it was deleted")
	}
}

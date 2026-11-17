package httpapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

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
	// The delete request only queues the files; the reference scan runs later.
	if !testAttachmentExists(owner.ID, own) {
		t.Fatal("the delete request removed files itself instead of queueing them")
	}
	queued, _ := os.ReadDir(attachmentSweepQueueDir())
	if len(queued) != 4 {
		t.Fatalf("queued %d files, want 4 (own, peer's, forked, in room)", len(queued))
	}
	if removed := h.sweepQueuedAttachments(); removed != 2 {
		t.Errorf("sweep removed %d files, want 2", removed)
	}
	if left, _ := os.ReadDir(attachmentSweepQueueDir()); len(left) != 0 {
		t.Errorf("sweep left %d markers", len(left))
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
	h.sweepQueuedAttachments()
	if testAttachmentExists(owner.ID, forked) {
		t.Error("the file stayed after the last conversation listing it was deleted")
	}
}

// Deleting many conversations costs one reference scan per batch, not one
// per conversation, and a failed scan keeps the batch queued.
func TestAttachmentSweepBatchesDeletionsAndRetriesOnError(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	h, owner, _ := scheduleSessionFixture(t)
	now := time.Now().UTC()
	var ids []string
	for i := range 30 {
		id := fmt.Sprintf("%032x", i+1)
		ids = append(ids, id)
		writeTestAttachment(t, owner.ID, id)
		conv := domain.AssistantSession{ID: fmt.Sprintf("chat-batch-%d", i), ProjectID: "sess-project", Messages: []domain.AssistantMessage{
			{Role: "user", Content: "图", Attachments: []domain.AssistantAttachment{{ID: id, Name: "a.png"}}, CreatedAt: now},
		}}
		if err := h.repo.PutAssistantSession(owner.ID, conv); err != nil {
			t.Fatal(err)
		}
		if err := h.deleteConversation(owner.ID, conv.ID); err != nil {
			t.Fatal(err)
		}
	}
	// A bogus marker is dropped, never treated as a path.
	if err := os.WriteFile(filepath.Join(attachmentSweepQueueDir(), "1-..%2f..%2fetc"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	queries := 0
	countQueries := func(*gorm.DB) { queries++ }
	if err := h.repo.DB.Callback().Row().After("gorm:row").Register("count_sweep_queries", countQueries); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.repo.DB.Callback().Row().Remove("count_sweep_queries") })
	if removed := h.sweepQueuedAttachments(); removed != len(ids) {
		t.Fatalf("sweep removed %d of %d files", removed, len(ids))
	}
	if queries != 3 {
		t.Errorf("sweep ran %d reference scans for 30 deletions, want 3 (one per table)", queries)
	}
	if left, _ := os.ReadDir(attachmentSweepQueueDir()); len(left) != 0 {
		t.Errorf("sweep left %d markers", len(left))
	}

	writeTestAttachment(t, owner.ID, ids[0])
	h.queueConversationAttachments(domain.AssistantSession{ID: "chat-gone", UserID: owner.ID, Messages: []domain.AssistantMessage{{Attachments: []domain.AssistantAttachment{{ID: ids[0]}}}}})
	if err := h.repo.DB.Exec("ALTER TABLE chat_messages RENAME TO chat_messages_away").Error; err != nil {
		t.Fatal(err)
	}
	if removed := h.sweepQueuedAttachments(); removed != 0 || !testAttachmentExists(owner.ID, ids[0]) {
		t.Fatal("a sweep whose reference check failed removed files")
	}
	if left, _ := os.ReadDir(attachmentSweepQueueDir()); len(left) != 1 {
		t.Fatalf("a failed sweep dropped its queue: %d markers left", len(left))
	}
	if err := h.repo.DB.Exec("ALTER TABLE chat_messages_away RENAME TO chat_messages").Error; err != nil {
		t.Fatal(err)
	}
	if removed := h.sweepQueuedAttachments(); removed != 1 {
		t.Fatalf("the retried sweep removed %d files", removed)
	}
}

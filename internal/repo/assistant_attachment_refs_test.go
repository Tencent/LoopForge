package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The reference scan reports which ids any conversation, pending turn or room
// message lists, across more ids than fit one query.
func TestAttachmentsInUseScansEveryTableInChunks(t *testing.T) {
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			st := openExecutionTestStore(t, backend)
			suffix := time.Now().UnixNano()
			id := func(i int) string { return fmt.Sprintf("%016x%016x", suffix, i) }
			var ids []string
			for i := range payloadMentionsChunk + 6 {
				ids = append(ids, id(i))
			}
			user, ok := st.UserByLoginName("demo-owner")
			if !ok {
				if err := st.PutUser(domain.User{LoginName: "demo-owner"}); err != nil {
					t.Fatal(err)
				}
				user, _ = st.UserByLoginName("demo-owner")
			}
			sessionID := fmt.Sprintf("chat-refs-%d", suffix)
			last := len(ids) - 1
			if err := st.PutAssistantSession(user.ID, domain.AssistantSession{ID: sessionID, Messages: []domain.AssistantMessage{
				{Role: "user", Content: "x", Attachments: []domain.AssistantAttachment{{ID: ids[1]}, {ID: ids[last]}}},
			}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.DeleteAssistantSession(user.ID, sessionID) })
			roomMessage := fmt.Sprintf("refs-msg-%d", suffix)
			if err := st.PutMessage(domain.ChatMessage{ID: roomMessage, RoomID: "refs-room", UserID: user.ID, Kind: domain.ChatKindText, Body: "x", Attachments: []domain.AssistantAttachment{{ID: ids[3]}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.DB.Table("chat_messages").Where("id = ?", roomMessage).Delete(nil).Error })

			inUse, err := st.AssistantAttachmentsInUse(ids)
			if err != nil {
				t.Fatal(err)
			}
			if len(inUse) != 2 || !inUse[ids[1]] || !inUse[ids[last]] {
				t.Fatalf("conversation references = %v", inUse)
			}
			rooms, err := st.RoomAttachmentsInUse(ids)
			if err != nil {
				t.Fatal(err)
			}
			if len(rooms) != 1 || !rooms[ids[3]] {
				t.Fatalf("room references = %v", rooms)
			}
		})
	}
}

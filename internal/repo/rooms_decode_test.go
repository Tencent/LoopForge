package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestDecodeChatMessageBackfillsIDAndRoomID(t *testing.T) {
	item, ok := decodeChatMessage(chatMessageRow{ID: "m1", RoomID: "r1", Payload: []byte(`{"text":"hi"}`)})
	if !ok || item.ID != "m1" || item.RoomID != "r1" {
		t.Fatalf("decodeChatMessage() = %+v, %v; want ID/RoomID backfilled", item, ok)
	}

	item, ok = decodeChatMessage(chatMessageRow{ID: "m1", RoomID: "r1", Payload: []byte(`{"id":"keep","roomId":"room"}`)})
	if !ok || item.ID != "keep" || item.RoomID != "room" {
		t.Fatalf("decodeChatMessage() = %+v, %v; want payload IDs kept", item, ok)
	}

	if item, ok := decodeChatMessage(chatMessageRow{ID: "m1", Payload: []byte(`{`)}); ok || item.ID != "" {
		t.Fatalf("decodeChatMessage(bad payload) = %+v, %v; want zero, false", item, ok)
	}
}

func TestDecodeChatMessagesReversedFlipsOrderAndSkipsBadRows(t *testing.T) {
	rows := []chatMessageRow{
		{ID: "new", RoomID: "r", Payload: []byte(`{}`)},
		{ID: "bad", RoomID: "r", Payload: []byte(`not json`)},
		{ID: "old", RoomID: "r", Payload: []byte(`{}`)},
	}
	got := decodeChatMessagesReversed(rows)
	want := []domain.ChatMessage{{ID: "old", RoomID: "r"}, {ID: "new", RoomID: "r"}}
	if len(got) != len(want) {
		t.Fatalf("decodeChatMessagesReversed() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].RoomID != want[i].RoomID {
			t.Fatalf("decodeChatMessagesReversed()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := decodeChatMessagesReversed(nil); got == nil || len(got) != 0 {
		t.Fatalf("decodeChatMessagesReversed(nil) = %#v, want empty non-nil", got)
	}
}

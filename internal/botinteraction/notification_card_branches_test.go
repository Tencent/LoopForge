package botinteraction

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gorilla/websocket"
)

// activeNotificationDelivery stores a delivery whose node still waits for
// approval, so ActionsForDelivery offers actions and the card is not expired.
func activeNotificationDelivery(t *testing.T, st *repo.Store) domain.NotificationDelivery {
	t.Helper()
	run := domain.Run{
		ID: "send-card-run", Status: "waiting",
		WorkflowSnapshot: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "n"}}},
		Nodes:            map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "waiting_approval", Attempt: 1}},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	delivery := domain.NotificationDelivery{
		ID: "send-card-delivery", BotID: "bot-send", ProjectID: "p", RunID: run.ID, NodeID: "n",
		Attempt: 1, EventType: "waiting", TargetType: "group", TargetID: "chat", Status: "sent", CreatedAt: time.Now(),
	}
	if err := st.DB.Create(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	if len(ActionsForDelivery(st, delivery)) == 0 {
		t.Fatal("fixture delivery must offer actions")
	}
	return delivery
}

// queueActiveCard enqueues a notification card for delivery and moves it to
// status, mirroring a card claimed by process.
func queueActiveCard(t *testing.T, st *repo.Store, id, deliveryID, status, quote string) *domain.BotInteraction {
	t.Helper()
	item := domain.BotInteraction{ID: id, BotID: "bot-send", SenderID: "user", Action: "notification", DeliveryID: deliveryID, Quote: quote, CreatedAt: time.Now()}
	if inserted, err := st.EnqueueBotInteraction(item, "group", "chat"); err != nil || !inserted {
		t.Fatalf("enqueue %s = %v, %v", id, inserted, err)
	}
	if err := st.DB.Model(&domain.BotInteraction{}).Where("id = ?", id).Update("status", status).Error; err != nil {
		t.Fatal(err)
	}
	item.Status = status
	return &item
}

// cardSocket dials a fake WeCom endpoint that rejects aibot_send_msg for chat
// "rejected" with errcode 400 and accepts every other send, recording bodies.
func cardSocket(t *testing.T) (*socket, func() []map[string]any) {
	t.Helper()
	var (
		mu    sync.Mutex
		sends []map[string]any
	)
	var upgrader websocket.Upgrader
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			var frame Frame
			if ws.ReadJSON(&frame) != nil {
				return
			}
			code := 0
			if frame.Cmd == "aibot_send_msg" {
				var body map[string]any
				_ = json.Unmarshal(frame.Body, &body)
				mu.Lock()
				sends = append(sends, body)
				mu.Unlock()
				if body["chatid"] == "rejected" {
					code = 400
				}
			}
			_ = ws.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": code})
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sock, err := dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "bot", "secret", func(*socket, Frame) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sock.close)
	return sock, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), sends...)
	}
}

func TestSendNotificationCardRejectsInvalidCardBeforeSending(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	delivery := activeNotificationDelivery(t, st)
	item := queueActiveCard(t, st, "send-invalid", delivery.ID, "running", "not json")
	// A nil socket proves an invalid card never reaches the transport.
	if err := New(st).sendNotificationCard(context.Background(), nil, item, domain.BotConversation{ChatID: "chat"}); err == nil || err.Error() != "通知卡片无效" {
		t.Fatalf("invalid card = %v", err)
	}
	if row := interactionRecord(t, st, item.ID); row.Status != "running" || row.CardTaskID != "" {
		t.Fatalf("invalid card must stay untouched: %#v", row)
	}
}

func TestSendNotificationCardDeliversAndRecordsOutcome(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	delivery := activeNotificationDelivery(t, st)
	svc := New(st)
	sock, sends := cardSocket(t)
	quote := `{"card_type":"button_interaction","main_title":{"title":"Review"}}`

	// A running card is first saved as ready, then sent and marked replied.
	sent := queueActiveCard(t, st, "send-ok", delivery.ID, "running", quote)
	if err := svc.sendNotificationCard(context.Background(), sock, sent, domain.BotConversation{ChatID: "chat"}); err != nil {
		t.Fatalf("send = %v", err)
	}
	row := interactionRecord(t, st, sent.ID)
	if row.Status != "replied" || !strings.HasPrefix(row.CardTaskID, delivery.ID+"_") || len(row.CardTaskID) != len(delivery.ID)+13 {
		t.Fatalf("sent card row = %#v", row)
	}
	got := sends()
	if len(got) != 1 || got[0]["chatid"] != "chat" || got[0]["msgtype"] != "template_card" {
		t.Fatalf("sent bodies = %#v", got)
	}
	card, _ := got[0]["template_card"].(map[string]any)
	if card["task_id"] != row.CardTaskID || card["card_type"] != "button_interaction" {
		t.Fatalf("sent card = %#v, task %q", card, row.CardTaskID)
	}

	// Resending the same card uses a fresh task_id suffix.
	again := queueActiveCard(t, st, "send-again", delivery.ID, "ready", quote)
	if err := svc.sendNotificationCard(context.Background(), sock, again, domain.BotConversation{ChatID: "chat"}); err != nil {
		t.Fatalf("resend = %v", err)
	}
	if next := interactionRecord(t, st, again.ID); next.Status != "replied" || next.CardTaskID == row.CardTaskID || !strings.HasPrefix(next.CardTaskID, delivery.ID+"_") {
		t.Fatalf("resent card row = %#v, first task %q", next, row.CardTaskID)
	}

	// A business rejection is recorded on the card and not returned.
	rejected := queueActiveCard(t, st, "send-rejected", delivery.ID, "ready", quote)
	if err := svc.sendNotificationCard(context.Background(), sock, rejected, domain.BotConversation{ChatID: "rejected"}); err != nil {
		t.Fatalf("business rejection must not be returned: %v", err)
	}
	if row := interactionRecord(t, st, rejected.ID); row.Status != "reply_failed" || row.LastError != (&ReplyError{Code: 400}).Error() || row.CardTaskID == "" {
		t.Fatalf("rejected card row = %#v", row)
	}

	// A transport failure is returned and leaves the card ready for retry.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	broken := queueActiveCard(t, st, "send-broken", delivery.ID, "ready", quote)
	if err := svc.sendNotificationCard(canceled, sock, broken, domain.BotConversation{ChatID: "chat"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("transport failure = %v", err)
	}
	if row := interactionRecord(t, st, broken.ID); row.Status != "ready" || row.LastError != "" {
		t.Fatalf("transport failure must leave card ready: %#v", row)
	}
	if n := len(sends()); n != 3 {
		t.Fatalf("send attempts = %d, want 3", n)
	}
}

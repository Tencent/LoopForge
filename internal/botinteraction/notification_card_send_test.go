package botinteraction

import (
	"context"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func queueNotificationInteraction(t *testing.T, st *repo.Store, id, action string) *domain.BotInteraction {
	t.Helper()
	item := domain.BotInteraction{ID: id, BotID: "bot-send", SenderID: "user", Action: action, DeliveryID: "missing-" + id, CreatedAt: time.Now()}
	if inserted, err := st.EnqueueBotInteraction(item, "single", "user"); err != nil || !inserted {
		t.Fatalf("enqueue %s = %v, %v", id, inserted, err)
	}
	return &item
}

func interactionRecord(t *testing.T, st *repo.Store, id string) domain.BotInteraction {
	t.Helper()
	var row domain.BotInteraction
	if err := st.DB.Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestExpireInactiveNotificationExpiresMissingDelivery(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	svc := New(st)
	item := queueNotificationInteraction(t, st, "expire-missing", "notification")
	expired, err := svc.expireInactiveNotification(item)
	if err != nil || !expired {
		t.Fatalf("expireInactiveNotification = %v, %v", expired, err)
	}
	if row := interactionRecord(t, st, item.ID); row.Status != "replied" || row.LastError == "" {
		t.Fatalf("expired card row = %#v", row)
	}
}

func TestSendNotificationCardSkipsInactiveDeliveryWithoutSocket(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	svc := New(st)
	item := queueNotificationInteraction(t, st, "send-missing", "notification")
	// A nil socket proves an inactive card never reaches the transport.
	if err := svc.sendNotificationCard(context.Background(), nil, item, domain.BotConversation{ChatID: "chat"}); err != nil {
		t.Fatalf("sendNotificationCard = %v", err)
	}
	if row := interactionRecord(t, st, item.ID); row.Status != "replied" || row.CardTaskID != "" {
		t.Fatalf("inactive card row = %#v", row)
	}
}

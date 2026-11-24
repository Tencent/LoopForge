package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"errors"
	"testing"
	"time"
)

type notificationTestConn struct {
	*fakeConn
}

func (c notificationTestConn) sendCard(ctx context.Context, chatID string, _ map[string]any) SendResult {
	return c.Reply(ctx, OutboundMessage{ChatID: chatID})
}

func (c notificationTestConn) updateActionCard(context.Context, domain.BotInteraction, string, bool) error {
	return nil
}

func TestWeComQueuedPrivateNotificationRechecksAccess(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := New(store)
	bot := domain.NotificationBot{ID: "private-origin", BotID: "private-bot", ProjectID: "p", Enabled: true, InteractionEnabled: true}
	if err := store.SaveNotificationBot(bot, "test-secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutUser(domain.User{ID: 42, LoginName: "member"}); err != nil {
		t.Fatal(err)
	}
	project := domain.Project{ID: bot.ProjectID, OwnerID: 1, Members: []domain.ProjectMember{{UserID: 42, Role: "member"}}}
	if err := store.PutProject(project); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token, err := store.NewBotBindingChallenge(bot, "member", now, true)
	if err != nil {
		t.Fatal(err)
	}
	code, err := store.ClaimBotBindingChallenge(token, 42, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmBotUserBinding(bot, "member", code, now); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "private-run", Status: "waiting", Nodes: map[string]*domain.NodeRun{
		"review": {NodeID: "review", Status: "waiting_approval", Attempt: 1},
	}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	delivery := domain.NotificationDelivery{
		ID: "private-notification", ProjectID: bot.ProjectID, BotID: bot.ID,
		RunID: run.ID, NodeID: "review", Attempt: 1, EventType: "waiting",
		Origin: domain.NotificationOriginChat, OriginUserID: 42, TargetID: "member", TargetType: "single",
		Status: "sending", CreatedAt: now, NextAttemptAt: now,
	}
	if err := store.CreateNotificationDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckWeComOriginTarget(bot, delivery); err != nil {
		t.Fatal(err)
	}

	actions := []string{ActionChatPost, "notification", "notification_details"}
	for _, action := range actions {
		item := domain.BotInteraction{
			ID: notificationInteractionID(bot.ID, action, delivery), BotID: bot.ID, DeliveryID: delivery.ID,
			Action: action, Content: "notification", Quote: "{}",
		}
		if _, err := store.EnqueueBotInteraction(item, delivery.TargetType, delivery.TargetID); err != nil {
			t.Fatal(err)
		}
	}
	project.Members = []domain.ProjectMember{}
	if err := store.PutProject(project); err != nil {
		t.Fatal(err)
	}

	lease, ok, err := store.ClaimBotConnection(bot.LeaseKey(), "worker")
	if !ok || err != nil {
		t.Fatalf("claim lease = %t, %v", ok, err)
	}
	conn := notificationTestConn{&fakeConn{channel: newFakeChannel(), done: make(chan struct{}), replies: make(chan OutboundMessage, len(actions))}}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	done := make(chan error, 1)
	go func() { done <- svc.process(ctx, bot, lease, conn, sendPolicy(bot)) }()
	defer func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("worker returned %v", err)
		}
	}()
	for _, action := range actions {
		id := notificationInteractionID(bot.ID, action, delivery)
		if _, err := svc.waitSent(ctx, id, action); !errors.Is(err, ErrWeComOriginUnavailable) {
			t.Errorf("%s send result = %v", action, err)
		}
	}
	if len(conn.replies) != 0 {
		t.Fatal("private notification reached the adapter after access was removed")
	}
}

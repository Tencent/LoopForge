package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"errors"
	"testing"
	"time"
)

// pushKindChannel adds proactive sends to the fake receiving channel.
type pushKindChannel struct {
	kindChannel
	pushes chan OutboundMessage
}

func (p pushKindChannel) Push(_ context.Context, _ domain.NotificationBot, _ Credential, msg OutboundMessage) SendResult {
	p.pushes <- msg
	return SendResult{Outcome: Delivered, PlatformMessageID: "pqk"}
}

// An expired DingTalk reply is switched to a proactive private message only
// for a verified, still-bound original sender; the saved reply goes out as a
// new generation with a new key through PushSender, not the webhook.
func TestDingTalkExpiredReplyResentAsPush(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.PutUser(domain.User{ID: 43, LoginName: "member"})
	store.PutProject(domain.Project{ID: "p", OwnerID: 42, Members: []domain.ProjectMember{{UserID: 43, Role: "member"}}})
	bot := domain.NotificationBot{ID: "cfg-dt-push", Channel: domain.BotChannelDingTalk, BotID: "ding-app", ProjectID: "p", OwnerID: 42, Enabled: true, InteractionEnabled: true, TenantKey: "ding-corp", TenantState: domain.BotTenantLocked}
	if _, err := store.SaveNotificationBotChecked(bot, "secret", nil); err != nil {
		t.Fatal(err)
	}
	bot, _ = store.NotificationBot(bot.ID)
	now := time.Now().UTC()
	token, _ := store.NewBotBindingChallenge(bot, "staff-1", now, true)
	code, _ := store.ClaimBotBindingChallenge(token, 43, now)
	if _, err := store.ConfirmBotUserBinding(bot, "staff-1", code, now); err != nil {
		t.Fatal(err)
	}
	service := New(store)
	fake := newFakeChannel()
	ch := pushKindChannel{kindChannel{fake, ChannelDingTalk}, make(chan OutboundMessage, 4)}
	service.RegisterChannel(ch)
	service.Generate = func(ctx context.Context, b domain.NotificationBot, c domain.BotConversation, h []domain.BotInteraction, item domain.BotInteraction) (string, error) {
		return "答复：" + item.Content, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	service.Start(ctx)
	defer func() { cancel(); <-service.Done }()
	conn := waitConn(t, fake)
	conn.push(t, InboundEvent{Channel: ChannelDingTalk, AppID: "ding-app", TenantKey: "ding-corp", EventID: "f-x", MessageID: "mx", SenderID: "staff-1", ChatID: "cid-1", ChatType: "single", MsgType: "text", Text: "进度", Route: ReplyRoute{Webhook: "https://oapi.dingtalk.com/robot/sendBySession?session=s", ExpiresAt: time.Now().Add(5 * time.Second)}})
	id := repo.BotMessageID(bot.ID, "mx")
	eventually(t, func() bool { it, _ := store.BotInteraction(id); return it.Status == domain.BotReplyFailed })
	expired, _ := store.BotInteraction(id)
	if expired.TerminalReason != "route_expired" {
		t.Fatalf("expired = %+v", expired)
	}
	// Not yet a verified push target.
	if _, err := store.PushResendBotInteraction(id, 42, 43, "k-1", "x"); !errors.Is(err, repo.ErrBotPushTargetUnverified) {
		t.Fatalf("unverified = %v", err)
	}
	if err := store.MarkBotPushTargetVerified(bot, "staff-1", 43, now); err != nil {
		t.Fatal(err)
	}
	// A different platform user than the binding is refused.
	if _, err := store.PushResendBotInteraction(id, 42, 44, "k-2", "x"); !errors.Is(err, repo.ErrBotPushTargetUnverified) {
		t.Fatalf("other user = %v", err)
	}
	out, err := store.PushResendBotInteraction(id, 42, 43, "k-3", "会话回复已过期")
	if err != nil || out.SendRoute != repo.BotSendRoutePush || out.DeliveryGeneration != expired.DeliveryGeneration+1 || out.SendKey == expired.SendKey {
		t.Fatalf("push resend = %+v %v", out, err)
	}
	select {
	case msg := <-ch.pushes:
		if msg.ChatID != "staff-1" || msg.Text != "答复：进度" || msg.SendKey != out.SendKey {
			t.Fatalf("push = %+v", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("expired reply not pushed")
	}
	eventually(t, func() bool { it, _ := store.BotInteraction(id); return it.Status == domain.BotReplied })
	final, _ := store.BotInteraction(id)
	if final.ResentBy != 42 || final.SendRoute != repo.BotSendRoutePush {
		t.Fatalf("final = %+v", final)
	}
	// The same click again returns without a second send.
	if again, err := store.PushResendBotInteraction(id, 42, 43, "k-3", "x"); err != nil || again.Status != domain.BotReplied {
		t.Fatalf("repeat = %+v %v", again, err)
	}
	if _, err := store.PushResendBotInteraction(id, 42, 43, "k-4", "x"); !errors.Is(err, repo.ErrBotStateConflict) {
		t.Fatalf("after reply = %v", err)
	}
	select {
	case m := <-ch.pushes:
		t.Fatalf("second push %+v", m)
	case m := <-conn.replies:
		t.Fatalf("webhook used %+v", m)
	case <-time.After(500 * time.Millisecond):
	}
}

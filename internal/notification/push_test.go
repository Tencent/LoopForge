package notification

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePushChannel is a Feishu or DingTalk adapter that only pushes.
type fakePushChannel struct {
	kind    string
	mu      sync.Mutex
	results []botinteraction.SendResult
	sent    []botinteraction.OutboundMessage
}

func (f *fakePushChannel) Kind() string { return f.kind }
func (f *fakePushChannel) Verify(context.Context, domain.NotificationBot, botinteraction.Credential) (botinteraction.Verification, error) {
	return botinteraction.Verification{}, nil
}
func (f *fakePushChannel) Connect(context.Context, domain.NotificationBot, botinteraction.Credential, botinteraction.InboundSink) (botinteraction.Conn, error) {
	return nil, errors.New("not used")
}
func (f *fakePushChannel) Push(_ context.Context, _ domain.NotificationBot, _ botinteraction.Credential, msg botinteraction.OutboundMessage) botinteraction.SendResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	if len(f.results) == 0 {
		return botinteraction.SendResult{Outcome: botinteraction.Delivered, PlatformMessageID: "pm-" + msg.SendKey[:6]}
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r
}
func (f *fakePushChannel) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// pushFixture: a notification-only (interaction disabled) configuration with
// a locked enterprise and one bound member; the run has a waiting, a
// succeeded and a failed event bound to the Bot.
func pushFixture(t *testing.T, channel string) (*Service, *fakePushChannel, domain.NotificationBot) {
	t.Helper()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	store.PutUser(domain.User{ID: 42, LoginName: "owner"})
	store.PutUser(domain.User{ID: 43, LoginName: "member", ChineseName: "成员"})
	store.PutProject(domain.Project{ID: "p", OwnerID: 42, Members: []domain.ProjectMember{{UserID: 43, Role: "member"}}})
	svc := New(store)
	svc.PublicURL = "https://platform.example"
	fake := &fakePushChannel{kind: channel}
	svc.Interactions.RegisterChannel(fake)
	bot := domain.NotificationBot{ID: "cfg-" + channel, ProjectID: "p", OwnerID: 42, Name: "通知", Channel: channel, BotID: "app-" + channel, Enabled: true, TenantKey: "tenant", TenantState: domain.BotTenantLocked}
	if err = store.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	bot, _ = store.NotificationBot(bot.ID)
	now := time.Now().UTC()
	token, err := store.NewBotBindingChallenge(bot, "member-im", now, true)
	if err != nil {
		t.Fatal(err)
	}
	code, err := store.ClaimBotBindingChallenge(token, 43, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConfirmBotUserBinding(bot, "member-im", code, now); err != nil {
		t.Fatal(err)
	}
	return svc, fake, bot
}

func verifyTarget(t *testing.T, svc *Service, bot domain.NotificationBot) domain.NotificationBot {
	t.Helper()
	target, err := svc.VerifyPushTarget(context.Background(), bot, "member-im")
	if err != nil {
		t.Fatal(err)
	}
	bot.Target = &target
	if err := svc.Store.SaveNotificationBot(bot, ""); err != nil {
		t.Fatal(err)
	}
	bot, _ = svc.Store.NotificationBot(bot.ID)
	return bot
}

// putPushRun records the given events: gate.waiting and node.failed are node
// reminders, run.succeeded is the SOP-level completion notification.
func putPushRun(t *testing.T, svc *Service, bot domain.NotificationBot, events ...string) domain.Run {
	t.Helper()
	run := domain.Run{ID: "r", ExecutionContext: domain.TelemetryContext{ProjectID: "p"}, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}
	run.WorkflowSnapshot.Name = "交付"
	run.WorkflowSnapshot.Notifications = []domain.NotificationBinding{{BotID: bot.ID, Scope: "run", Events: []string{"succeeded", "failed", "waiting"}, Template: "### {{status}}\nSOP：**{{workflow}}**\n[查看详情]({{url}})"}}
	for i, ev := range events {
		id := string(rune('a' + i))
		nodeID := id
		if ev == "run.succeeded" {
			nodeID = ""
		} else {
			run.WorkflowSnapshot.Nodes = append(run.WorkflowSnapshot.Nodes, domain.WorkflowNode{ID: id, Name: "节点" + id, Type: "agent"})
			status := map[string]string{"gate.waiting": "waiting_approval", "node.failed": "failed"}[ev]
			run.Nodes[id] = &domain.NodeRun{NodeID: id, Status: status, Attempt: 1}
		}
		run.Events = append(run.Events, domain.RunEvent{ID: "e" + id, NodeID: nodeID, Type: ev, Timestamp: time.Now().UTC()})
	}
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}

func deliveries(t *testing.T, svc *Service) map[string]domain.NotificationDelivery {
	t.Helper()
	items, err := svc.Store.NotificationDeliveries("p")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]domain.NotificationDelivery{}
	for _, item := range items {
		if item.EventType != "test" {
			out[item.EventType] = item
		}
	}
	return out
}

func drain(t *testing.T, svc *Service) {
	t.Helper()
	for i := 0; i < 10; i++ {
		ok, err := svc.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return
		}
	}
}

func TestPushTargetRequiresBindingAndSuccessfulTrial(t *testing.T) {
	svc, fake, bot := pushFixture(t, domain.BotChannelDingTalk)
	targets, err := svc.Sessions(context.Background(), bot)
	if err != nil || len(targets) != 1 || targets[0].ID != "member-im" || targets[0].VerifiedAt != nil || !strings.Contains(targets[0].Name, "成员（member）") {
		t.Fatalf("targets = %+v %v", targets, err)
	}
	if _, err := svc.VerifyPushTarget(context.Background(), bot, "stranger"); !errors.Is(err, ErrPushTargetUnverified) {
		t.Fatalf("unbound target = %v", err)
	}
	fake.results = []botinteraction.SendResult{{Outcome: botinteraction.Unknown, SafeDetail: "未知"}}
	var trial *PushTargetError
	if _, err := svc.VerifyPushTarget(context.Background(), bot, "member-im"); !errors.As(err, &trial) {
		t.Fatalf("unknown trial verified: %v", err)
	}
	if targets, _ := svc.Sessions(context.Background(), bot); targets[0].VerifiedAt != nil {
		t.Fatal("unknown trial marked target verified")
	}
	bot = verifyTarget(t, svc, bot)
	if bot.Target == nil || bot.Target.VerifiedAt == nil {
		t.Fatalf("target = %+v", bot.Target)
	}
	if fake.sent[len(fake.sent)-1].ChatID != "member-im" {
		t.Fatal("trial went to the wrong target")
	}
	// Unlocked enterprises cannot list targets.
	other := bot
	other.TenantState = domain.BotTenantAwaiting
	if _, err := svc.Sessions(context.Background(), other); err == nil {
		t.Fatal("unlocked tenant listed targets")
	}
}

func TestPushSendsAllSOPEventsForNotificationOnlyBot(t *testing.T) {
	for _, channel := range []string{domain.BotChannelFeishu, domain.BotChannelDingTalk} {
		t.Run(channel, func(t *testing.T) {
			svc, fake, bot := pushFixture(t, channel)
			bot = verifyTarget(t, svc, bot)
			trials := fake.count()
			putPushRun(t, svc, bot, "gate.waiting", "run.succeeded", "node.failed")
			drain(t, svc)
			got := deliveries(t, svc)
			for _, ev := range []string{"waiting", "succeeded", "failed"} {
				item := got[ev]
				if item.Status != "sent" || item.ReceiptSource != "platform" || item.DeliveryGeneration != 1 || item.SendKey == "" {
					t.Fatalf("%s = %+v", ev, item)
				}
			}
			if fake.count()-trials != 3 {
				t.Fatalf("pushes = %d", fake.count()-trials)
			}
			for _, msg := range fake.sent[trials:] {
				if msg.ChatID != "member-im" || strings.Contains(msg.Text, "**") || strings.Contains(msg.Text, "](") {
					t.Fatalf("push message = %+v", msg)
				}
			}
		})
	}
}

func TestPushOutcomesFollowChannelIdempotency(t *testing.T) {
	t.Run("feishu unknown retries with the same key", func(t *testing.T) {
		svc, fake, bot := pushFixture(t, domain.BotChannelFeishu)
		bot = verifyTarget(t, svc, bot)
		putPushRun(t, svc, bot, "run.succeeded")
		trials := fake.count()
		fake.results = []botinteraction.SendResult{{Outcome: botinteraction.Unknown, SafeDetail: "响应丢失"}}
		drain(t, svc)
		item := deliveries(t, svc)["succeeded"]
		if item.Status != "pending" || item.SendStartedAtUS == 0 {
			t.Fatalf("after unknown = %+v", item)
		}
		forceDue(t, svc, item.ID)
		drain(t, svc)
		item = deliveries(t, svc)["succeeded"]
		sent := fake.sent[trials:]
		if item.Status != "sent" || len(sent) != 2 || sent[0].SendKey != sent[1].SendKey {
			t.Fatalf("retry = %+v keys %v", item, sent)
		}
	})
	t.Run("feishu keeps the first unknown send through a failed retry", func(t *testing.T) {
		svc, fake, bot := pushFixture(t, domain.BotChannelFeishu)
		bot = verifyTarget(t, svc, bot)
		putPushRun(t, svc, bot, "run.succeeded")
		trials := fake.count()
		fake.results = []botinteraction.SendResult{{Outcome: botinteraction.Unknown, SafeDetail: "响应丢失"}, {Outcome: botinteraction.NotDelivered, Retryable: true, SafeDetail: "连接失败"}}
		drain(t, svc)
		first := deliveries(t, svc)["succeeded"]
		forceDue(t, svc, first.ID)
		drain(t, svc)
		item := deliveries(t, svc)["succeeded"]
		if item.Status != "pending" || item.SendStartedAtUS != first.SendStartedAtUS {
			t.Fatalf("after a failed retry = %+v, first send at %d", item, first.SendStartedAtUS)
		}
		// Past the de-duplication window a retry could send it twice.
		if err := svc.Store.DB.Model(&domain.NotificationDelivery{}).Where("id = ?", item.ID).Update("send_started_at_us", item.SendStartedAtUS-int64(pushIdempotencyWindow/time.Microsecond)-1).Error; err != nil {
			t.Fatal(err)
		}
		forceDue(t, svc, item.ID)
		drain(t, svc)
		if item = deliveries(t, svc)["succeeded"]; item.Status != "send_unknown" || fake.count()-trials != 2 {
			t.Fatalf("past the window = %+v pushes %d", item, fake.count()-trials)
		}
	})
	t.Run("feishu rejected retry keeps unknown for review", func(t *testing.T) {
		svc, fake, bot := pushFixture(t, domain.BotChannelFeishu)
		bot = verifyTarget(t, svc, bot)
		putPushRun(t, svc, bot, "run.succeeded")
		fake.results = []botinteraction.SendResult{{Outcome: botinteraction.Unknown}, {Outcome: botinteraction.Rejected}}
		drain(t, svc)
		item := deliveries(t, svc)["succeeded"]
		forceDue(t, svc, item.ID)
		drain(t, svc)
		if got := deliveries(t, svc)["succeeded"]; got.Status != "send_unknown" || got.SendStartedAtUS != item.SendStartedAtUS {
			t.Fatalf("after rejected retry = %+v", got)
		}
		if err := svc.Store.ConfirmNotificationReceived(item.ProjectID, item.ID, 42); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("dingtalk unknown stops for review and can be confirmed", func(t *testing.T) {
		svc, fake, bot := pushFixture(t, domain.BotChannelDingTalk)
		bot = verifyTarget(t, svc, bot)
		putPushRun(t, svc, bot, "node.failed")
		trials := fake.count()
		fake.results = []botinteraction.SendResult{{Outcome: botinteraction.Unknown, SafeDetail: "钉钉返回服务端错误，送达结果未知"}}
		drain(t, svc)
		item := deliveries(t, svc)["failed"]
		if item.Status != "send_unknown" || fake.count()-trials != 1 {
			t.Fatalf("unknown = %+v", item)
		}
		if err := svc.Store.ConfirmNotificationReceived("p", item.ID, 42); err != nil {
			t.Fatal(err)
		}
		item = deliveries(t, svc)["failed"]
		if item.Status != "sent" || item.ReceiptSource != "manual" || item.ConfirmedBy != 42 {
			t.Fatalf("confirmed = %+v", item)
		}
	})
	t.Run("manual resend starts a new generation", func(t *testing.T) {
		svc, fake, bot := pushFixture(t, domain.BotChannelDingTalk)
		bot = verifyTarget(t, svc, bot)
		putPushRun(t, svc, bot, "node.failed")
		trials := fake.count()
		fake.results = []botinteraction.SendResult{{Outcome: botinteraction.Unknown}}
		drain(t, svc)
		item := deliveries(t, svc)["failed"]
		if err := svc.Store.RetryNotification("p", item.ID); err != nil {
			t.Fatal(err)
		}
		drain(t, svc)
		item = deliveries(t, svc)["failed"]
		sent := fake.sent[trials:]
		if item.Status != "sent" || item.DeliveryGeneration != 2 || len(sent) != 2 || sent[0].SendKey == sent[1].SendKey {
			t.Fatalf("resend = %+v %v", item, sent)
		}
	})
	t.Run("interrupted send", func(t *testing.T) {
		for channel, want := range map[string]string{domain.BotChannelDingTalk: "send_unknown", domain.BotChannelFeishu: "sent"} {
			svc, fake, bot := pushFixture(t, channel)
			bot = verifyTarget(t, svc, bot)
			putPushRun(t, svc, bot, "run.succeeded")
			// Claim and begin, then "crash" before the outcome is saved.
			item, err := svc.Store.ClaimNotification(time.Now().UTC(), "crashed")
			if err != nil || item == nil {
				t.Fatal(item, err)
			}
			if _, _, _, err := svc.Store.BeginNotificationPush(*item); err != nil {
				t.Fatal(err)
			}
			trials := fake.count()
			// The lease runs out; the next claim resumes the generation.
			if _, err := svc.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, _ := svc.Store.NotificationDelivery(item.ID)
			if got.Status != "sending" {
				t.Fatalf("%s: lease not held: %s", channel, got.Status)
			}
			expireLease(t, svc, item.ID)
			drain(t, svc)
			got, _ = svc.Store.NotificationDelivery(item.ID)
			pushed := fake.count() - trials
			if got.Status != want || (want == "send_unknown" && pushed != 0) || (want == "sent" && pushed != 1) {
				t.Fatalf("%s: interrupted = %s pushes %d", channel, got.Status, pushed)
			}
		}
	})
	t.Run("not delivered and rejected", func(t *testing.T) {
		svc, fake, bot := pushFixture(t, domain.BotChannelDingTalk)
		bot = verifyTarget(t, svc, bot)
		putPushRun(t, svc, bot, "run.succeeded", "node.failed")
		fake.results = []botinteraction.SendResult{{Outcome: botinteraction.NotDelivered, Retryable: true, SafeDetail: "限流"}, {Outcome: botinteraction.Rejected, Code: "invalid_staff", SafeDetail: "成员不可见"}}
		drain(t, svc)
		got := deliveries(t, svc)
		statuses := []string{got["succeeded"].Status, got["failed"].Status}
		if !(contains(statuses, "pending") && contains(statuses, "target_unavailable")) {
			t.Fatalf("statuses = %v", statuses)
		}
		for _, item := range got {
			if item.Status == "pending" && item.SendStartedAtUS != 0 {
				t.Fatal("not-delivered attempt kept its start time")
			}
		}
	})
}

func TestPushTargetRevocationStopsDelivery(t *testing.T) {
	svc, fake, bot := pushFixture(t, domain.BotChannelFeishu)
	bot = verifyTarget(t, svc, bot)
	putPushRun(t, svc, bot, "run.succeeded")
	binding, err := svc.Store.BotUserBinding(bot.IdentityScope(), "member-im")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.RevokeBotUserBinding(binding.ID, 43, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	trials := fake.count()
	drain(t, svc)
	item := deliveries(t, svc)["succeeded"]
	if item.Status != "target_unavailable" || fake.count() != trials {
		t.Fatalf("revoked target = %+v", item)
	}
	reread, _ := svc.Store.NotificationBot(bot.ID)
	if reread.Target == nil || reread.Target.VerifiedAt != nil {
		t.Fatalf("revoked target still shown verified: %+v", reread.Target)
	}
	// Re-binding requires a fresh trial send (outside the link rate limit).
	now := time.Now().UTC().Add(2 * time.Minute)
	token, _ := svc.Store.NewBotBindingChallenge(bot, "member-im", now, true)
	code, _ := svc.Store.ClaimBotBindingChallenge(token, 43, now)
	if _, err := svc.Store.ConfirmBotUserBinding(bot, "member-im", code, now); err != nil {
		t.Fatal(err)
	}
	if b, _ := svc.Store.BotUserBinding(bot.IdentityScope(), "member-im"); b.TargetVerifiedAt != nil {
		t.Fatal("re-binding kept the old verification")
	}
}

func TestPushWaitingNotificationExpiresWhenNodeMoves(t *testing.T) {
	svc, fake, bot := pushFixture(t, domain.BotChannelDingTalk)
	bot = verifyTarget(t, svc, bot)
	run := putPushRun(t, svc, bot, "gate.waiting")
	run.Nodes["a"].Status = "succeeded"
	run.Status = "succeeded"
	if err := svc.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	trials := fake.count()
	drain(t, svc)
	if item := deliveries(t, svc)["waiting"]; item.Status != "expired" || fake.count() != trials {
		t.Fatalf("stale waiting = %+v", item)
	}
}

func TestSwitchNotificationTarget(t *testing.T) {
	svc, _, bot := pushFixture(t, domain.BotChannelDingTalk)
	bot = verifyTarget(t, svc, bot)
	putPushRun(t, svc, bot, "run.succeeded", "node.failed")
	// Rows from before run origins existed have no origin at all.
	if err := svc.Store.DB.Model(&domain.NotificationDelivery{}).Where("bot_id = ?", bot.ID).Update("origin", nil).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	fromChat := domain.NotificationDelivery{ID: "from-chat", ProjectID: bot.ProjectID, BotID: bot.ID, EventType: "succeeded", SourceType: "run", SourceID: "r", Origin: domain.NotificationOriginChat, TargetID: "origin-group", TargetType: "group", Content: "### SOP 已完成", Status: "pending", CreatedAt: now, NextAttemptAt: now}
	if err := svc.Store.CreateNotificationDelivery(fromChat); err != nil {
		t.Fatal(err)
	}
	if n, _ := svc.Store.PendingNotificationsForOtherTargets(bot.ID, "new-target"); n != 2 {
		t.Fatalf("pending = %d", n)
	}
	if n, err := svc.Store.SwitchNotificationTarget(bot.ID, domain.BotChat{ID: "new-target", Name: "新", Type: "single"}, "migrate"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	for _, item := range deliveries(t, svc) {
		if item.ID != fromChat.ID && (item.TargetID != "new-target" || item.DeliveryGeneration != 1 || !strings.Contains(item.LastError, "改发")) {
			t.Fatalf("migrated = %+v", item)
		}
	}
	if n, _ := svc.Store.SwitchNotificationTarget(bot.ID, domain.BotChat{ID: "third"}, "cancel"); n != 2 {
		t.Fatalf("cancelled = %d", n)
	}
	for _, item := range deliveries(t, svc) {
		if item.ID != fromChat.ID && item.Status != "cancelled" {
			t.Fatalf("cancel = %+v", item)
		}
	}
	if got, _ := svc.Store.NotificationDelivery(fromChat.ID); got.Status != "pending" || got.TargetID != "origin-group" || got.DeliveryGeneration != 0 {
		t.Fatalf("message back to the chat = %+v", got)
	}
}

func TestPlainNotificationText(t *testing.T) {
	in := "### SOP 已完成\n> **交付** 已完成\n<font color=\"info\">成功</font>\n[查看详情](https://platform.example/#/runs/r)（请处理）"
	got := plainNotificationText(in)
	want := "SOP 已完成\n交付 已完成\n成功\n查看详情：\nhttps://platform.example/#/runs/r\n（请处理）"
	if got != want {
		t.Fatalf("plain = %q", got)
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func forceDue(t *testing.T, svc *Service, id string) {
	t.Helper()
	if err := svc.Store.DB.Model(&domain.NotificationDelivery{}).Where("id = ?", id).Update("next_attempt_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
}

func expireLease(t *testing.T, svc *Service, id string) {
	t.Helper()
	if err := svc.Store.DB.Model(&domain.NotificationDelivery{}).Where("id = ?", id).Update("lease_until", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
}

func TestPushSendsActionCardOnlyWhenTheBotReceivesClicks(t *testing.T) {
	for _, interaction := range []bool{false, true} {
		svc, fake, bot := pushFixture(t, domain.BotChannelFeishu)
		bot = verifyTarget(t, svc, bot)
		bot.InteractionEnabled = interaction
		if err := svc.Store.SaveNotificationBot(bot, ""); err != nil {
			t.Fatal(err)
		}
		trials := fake.count()
		putPushRun(t, svc, bot, "gate.waiting", "node.failed", "run.succeeded")
		drain(t, svc)
		cards := 0
		for _, msg := range fake.sent[trials:] {
			if msg.Card != nil {
				cards++
				if len(msg.Card.Actions) == 0 || msg.Card.DeliveryID == "" || msg.Text == "" {
					t.Fatalf("card = %+v", msg.Card)
				}
			}
		}
		if want := map[bool]int{false: 0, true: 2}[interaction]; cards != want {
			t.Fatalf("interaction=%v cards = %d, want %d", interaction, cards, want)
		}
	}
}

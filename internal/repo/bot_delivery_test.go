package repo

import (
	"agentflow/internal/domain"
	"errors"
	"testing"
	"time"
)

const testClockStart = int64(1_800_000_000_000_000)

type deliveryFixture struct {
	t     *testing.T
	s     *Store
	bot   domain.NotificationBot
	lease BotLease
	clock func(int64)
}

func newDeliveryFixture(t *testing.T, channel string) *deliveryFixture {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	bot := domain.NotificationBot{ID: "cfg-" + channel, Channel: channel, BotID: "app-" + channel, ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	if err := s.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	bot, _ = s.NotificationBot(bot.ID)
	clock := pinDBClock(t, testClockStart)
	lease, ok, err := s.ClaimBotConnection(bot.LeaseKey(), "owner-1")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	return &deliveryFixture{t: t, s: s, bot: bot, lease: lease, clock: clock}
}

// advance moves the database clock, renewing the lease every 30s as the
// receiver does.
func (f *deliveryFixture) advance(d time.Duration) {
	f.t.Helper()
	left := int64(d / time.Microsecond)
	for {
		step := min(left, int64(30*time.Second/time.Microsecond))
		f.clock(step)
		left -= step
		if ok, err := f.s.RenewBotConnection(f.lease, "", ""); err != nil || !ok {
			f.t.Fatalf("renew while advancing %s: %v %v", d, ok, err)
		}
		if left == 0 {
			return
		}
	}
}

// ready enqueues a message and runs it to ready with a saved reply.
func (f *deliveryFixture) ready(msg string) domain.BotInteraction {
	f.t.Helper()
	item := domain.BotInteraction{ID: BotMessageID(f.bot.IdentityScope(), msg), BotID: f.bot.ID, ProjectID: "p", SenderID: "u", Content: msg}
	if _, err := f.s.EnqueueBotInteraction(item, "single", "u"); err != nil {
		f.t.Fatal(err)
	}
	// Older ready items are claimed first; keep claiming until ours runs.
	var claimed *domain.BotInteraction
	for i := 0; i < 10; i++ {
		got, err := f.s.ClaimBotInteraction(f.bot.ID, f.lease, sendPolicyOf(f.bot))
		if err != nil {
			f.t.Fatal(err)
		}
		if got != nil && got.ID == item.ID {
			claimed = got
			break
		}
		if got != nil {
			// Park the older ready item so the queue moves on.
			f.s.DB.Model(&domain.BotInteraction{}).Where("id = ?", got.ID).Update("next_send_at_us", testClockStart+int64(24*time.Hour/time.Microsecond))
		}
	}
	if claimed == nil || claimed.Status != domain.BotRunning {
		f.t.Fatalf("claim = %+v", claimed)
	}
	if err := f.s.SaveBotReply(*claimed, f.lease, "reply:"+msg, ""); err != nil {
		f.t.Fatal(err)
	}
	saved, _ := f.s.BotInteraction(item.ID)
	if saved.Status != domain.BotReady || saved.SendKey == "" || saved.DeliveryGeneration != 1 {
		f.t.Fatalf("saved = %+v", saved)
	}
	return saved
}

func sendPolicyOf(bot domain.NotificationBot) BotSendPolicy {
	return BotSendPolicy{Idempotent: bot.ChannelKind() == domain.BotChannelFeishu}
}

func (f *deliveryFixture) send(id, outcome string) domain.BotInteraction {
	f.t.Helper()
	if _, err := f.s.ClaimBotInteraction(f.bot.ID, f.lease, sendPolicyOf(f.bot)); err != nil {
		f.t.Fatal(err)
	}
	sending, err := f.s.BeginBotSend(id, f.lease, sendPolicyOf(f.bot))
	if err != nil {
		f.t.Fatalf("begin: %v", err)
	}
	item, err := f.s.FinishBotSend(id, f.lease, sendPolicyOf(f.bot), BotSendOutcome{Outcome: outcome, Detail: outcome, PlatformMessageID: "pm"})
	if err != nil {
		f.t.Fatalf("finish: %v", err)
	}
	stored, _ := f.s.BotInteraction(id)
	if stored.SendKey != sending.SendKey {
		f.t.Fatal("send key changed during automatic retries")
	}
	_ = item
	return stored
}

func TestBotDeliveryUndeliveredRetriesThenFails(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelWeCom)
	item := f.ready("m")
	waits := []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}
	for i, wait := range waits {
		got := f.send(item.ID, "not_delivered")
		if got.Status != domain.BotReady || got.UndeliveredRetries != i+1 {
			t.Fatalf("retry %d = %+v", i, got)
		}
		// Not due before the backoff ends.
		if claimed, _ := f.s.ClaimBotInteraction(f.bot.ID, f.lease, BotSendPolicy{}); claimed != nil {
			t.Fatalf("retry %d claimed before backoff", i)
		}
		f.advance(wait)
	}
	got := f.send(item.ID, "not_delivered")
	if got.Status != domain.BotReplyFailed || got.TerminalReason != "undelivered_exhausted" || got.SendAttempts != 4 {
		t.Fatalf("exhausted = %+v", got)
	}
}

func TestBotDeliveryRejectedAndDelivered(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelWeCom)
	rejected := f.send(f.ready("r").ID, "rejected")
	if rejected.Status != domain.BotReplyFailed || rejected.TerminalReason != "rejected" {
		t.Fatalf("rejected = %+v", rejected)
	}
	delivered := f.send(f.ready("d").ID, "delivered")
	if delivered.Status != domain.BotReplied || delivered.PlatformMessageID != "pm" || delivered.ReceiptSource != "platform" {
		t.Fatalf("delivered = %+v", delivered)
	}
}

func TestBotDeliveryUnknownWithoutIdempotencyNeedsReview(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelWeCom)
	got := f.send(f.ready("u").ID, "unknown")
	if got.Status != domain.BotReplyUnknown || got.TerminalReason != "delivery_unknown" {
		t.Fatalf("unknown = %+v", got)
	}
	if _, err := f.s.ConfirmBotInteraction(got.ID, 9, "confirm-1"); err != nil {
		t.Fatal(err)
	}
	confirmed, _ := f.s.BotInteraction(got.ID)
	if confirmed.Status != domain.BotReplied || confirmed.ReceiptSource != "manual" || confirmed.ConfirmedBy != 9 || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirmed = %+v", confirmed)
	}
	if _, err := f.s.ConfirmBotInteraction(got.ID, 9, "confirm-1"); err != nil {
		t.Fatalf("repeated confirm click: %v", err)
	}
	if _, err := f.s.ResendBotInteraction(got.ID, 9, "resend-x", ""); !errors.Is(err, ErrBotStateConflict) {
		t.Fatalf("resend of replied = %v", err)
	}
}

// Feishu de-duplicates the same uuid for an hour: unknown outcomes retry with
// the same key until first send + 55 minutes on the database clock, across
// owner changes, then stop at reply_unknown.
func TestBotDeliveryIdempotentWindowOnDatabaseClock(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelFeishu)
	item := f.ready("w")
	first := f.send(item.ID, "unknown")
	if first.Status != domain.BotReady || first.SendStartedAtUS != testClockStart {
		t.Fatalf("first unknown = %+v", first)
	}
	// Owner changes: the old owner stops, a new one recovers after expiry.
	f.clock(BotConnectionLeaseUS + 1)
	lease, ok, err := f.s.ClaimBotConnection(f.bot.LeaseKey(), "owner-2")
	if err != nil || !ok || lease.Epoch != f.lease.Epoch+1 {
		t.Fatalf("takeover = %+v %v %v", lease, ok, err)
	}
	if _, err := f.s.BeginBotSend(item.ID, f.lease, BotSendPolicy{Idempotent: true}); !errors.Is(err, ErrBotLeaseLost) {
		t.Fatalf("old owner began a send: %v", err)
	}
	f.lease = lease
	if err := f.s.RecoverBotInteractions(f.bot.ID, lease, BotSendPolicy{Idempotent: true}); err != nil {
		t.Fatal(err)
	}
	// 54:59 after the first send there is no room for a 10s call: closed.
	f.advance(54*time.Minute + 59*time.Second - time.Duration(BotConnectionLeaseUS+1)*time.Microsecond)
	f.s.DB.Model(&domain.BotInteraction{}).Where("id = ?", item.ID).Update("next_send_at_us", 0)
	if _, err := f.s.ClaimBotInteraction(f.bot.ID, lease, BotSendPolicy{Idempotent: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.BeginBotSend(item.ID, lease, BotSendPolicy{Idempotent: true}); !errors.Is(err, ErrBotStateConflict) {
		t.Fatalf("send inside the last call timeout: %v", err)
	}
	final, _ := f.s.BotInteraction(item.ID)
	if final.Status != domain.BotReplyUnknown || final.TerminalReason != "window_expired" || final.SendStartedAtUS != testClockStart {
		t.Fatalf("window = %+v", final)
	}
}

func TestBotDeliveryIdempotentRetryInsideWindowKeepsKey(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelFeishu)
	item := f.ready("k")
	first := f.send(item.ID, "unknown")
	f.advance(5 * time.Second)
	second := f.send(item.ID, "delivered")
	if second.Status != domain.BotReplied || second.SendKey != first.SendKey || second.SendStartedAtUS != testClockStart || second.SendAttempts != 2 {
		t.Fatalf("retry = %+v", second)
	}
}

func TestFeishuReplyRejectedRetryKeepsUnknown(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelFeishu)
	item := f.ready("first reply")
	if got := f.send(item.ID, "unknown"); got.Status != domain.BotReady {
		t.Fatalf("first send = %+v", got)
	}
	f.advance(10 * time.Second)
	if got := f.send(item.ID, "rejected"); got.Status != domain.BotReplyUnknown {
		t.Fatalf("rejected retry = %+v", got)
	}
	if _, err := f.s.ConfirmBotInteraction(item.ID, 42, "confirm"); err != nil {
		t.Fatal(err)
	}
}

func TestBotDeliveryCrashDuringSendRecoversAfterSendLease(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelWeCom)
	item := f.ready("c")
	if _, err := f.s.ClaimBotInteraction(f.bot.ID, f.lease, BotSendPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.BeginBotSend(item.ID, f.lease, BotSendPolicy{}); err != nil {
		t.Fatal(err)
	}
	// Same owner, send lease not over: nothing to resolve.
	if claimed, _ := f.s.ClaimBotInteraction(f.bot.ID, f.lease, BotSendPolicy{}); claimed != nil {
		t.Fatal("in-flight send claimed")
	}
	f.advance(time.Duration(BotSendLeaseUS) * time.Microsecond)
	if claimed, _ := f.s.ClaimBotInteraction(f.bot.ID, f.lease, BotSendPolicy{}); claimed != nil {
		t.Fatal("interrupted send returned as work")
	}
	got, _ := f.s.BotInteraction(item.ID)
	if got.Status != domain.BotReplyUnknown {
		t.Fatalf("interrupted send = %+v", got)
	}
	// The late result of the stopped sender cannot overwrite it.
	if _, err := f.s.FinishBotSend(item.ID, f.lease, BotSendPolicy{}, BotSendOutcome{Outcome: "delivered"}); !errors.Is(err, ErrBotLeaseLost) {
		t.Fatalf("late receipt = %v", err)
	}
}

func TestBotSendNeedsLeaseHeadroomAndValidRoute(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelWeCom)
	item := f.ready("h")
	f.clock(BotConnectionLeaseUS - BotSendMinLeaseUS + 1)
	if _, err := f.s.BeginBotSend(item.ID, f.lease, BotSendPolicy{}); !errors.Is(err, ErrBotLeaseLow) {
		t.Fatalf("low lease = %v", err)
	}
	f.advance(0)
	routed := f.ready("route")
	f.s.DB.Model(&domain.BotInteraction{}).Where("id = ?", routed.ID).Update("route_expires_at_us", testClockStart+int64(35*time.Second/time.Microsecond))
	if _, err := f.s.BeginBotSend(routed.ID, f.lease, BotSendPolicy{}); !errors.Is(err, ErrBotStateConflict) {
		t.Fatalf("expired route = %v", err)
	}
	expired, _ := f.s.BotInteraction(routed.ID)
	if expired.Status != domain.BotReplyFailed || expired.TerminalReason != "route_expired" {
		t.Fatalf("route = %+v", expired)
	}
	if _, err := f.s.ResendBotInteraction(routed.ID, 1, "k", ""); !errors.Is(err, ErrBotRouteExpired) {
		t.Fatalf("resend expired route = %v", err)
	}
}

func TestBotDisableKeepsReadyAndDeleteStopsWork(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelWeCom)
	ready := f.ready("ready")
	queued := domain.BotInteraction{ID: BotMessageID(f.bot.IdentityScope(), "queued"), BotID: f.bot.ID, SenderID: "u", Content: "q"}
	f.s.EnqueueBotInteraction(queued, "single", "u")
	sendingItem := f.ready("sending")
	f.s.ClaimBotInteraction(f.bot.ID, f.lease, BotSendPolicy{})
	// The oldest ready item is claimed first; begin the send of the third.
	f.s.DB.Model(&domain.BotInteraction{}).Where("id = ?", sendingItem.ID).Updates(map[string]any{"lease_owner": f.lease.Owner, "lease_epoch": f.lease.Epoch})
	if _, err := f.s.BeginBotSend(sendingItem.ID, f.lease, BotSendPolicy{}); err != nil {
		t.Fatal(err)
	}

	disabled := f.bot
	disabled.InteractionEnabled = false
	if err := f.s.SaveNotificationBot(disabled, ""); err != nil {
		t.Fatal(err)
	}
	status := func(id string) (string, string) {
		item, _ := f.s.BotInteraction(id)
		return item.Status, item.TerminalReason
	}
	if st, _ := status(ready.ID); st != domain.BotReady {
		t.Fatalf("disable dropped a saved reply: %s", st)
	}
	if st, reason := status(queued.ID); st != domain.BotCancelled || reason != "bot_disabled" {
		t.Fatalf("queued after disable = %s %s", st, reason)
	}
	if st, reason := status(sendingItem.ID); st != domain.BotReplyUnknown || reason != "bot_disabled_during_send" {
		t.Fatalf("sending after disable = %s %s", st, reason)
	}
	if _, err := f.s.FinishBotSend(sendingItem.ID, f.lease, BotSendPolicy{}, BotSendOutcome{Outcome: "delivered"}); !errors.Is(err, ErrBotLeaseLost) {
		t.Fatalf("late receipt after disable = %v", err)
	}

	if err := f.s.DeleteNotificationBot(f.bot.ID); err != nil {
		t.Fatal(err)
	}
	if st, reason := status(ready.ID); st != domain.BotCancelled || reason != "bot_deleted" {
		t.Fatalf("ready after delete = %s %s", st, reason)
	}
	if _, err := f.s.ResendBotInteraction(sendingItem.ID, 1, "k2", ""); !errors.Is(err, ErrBotDeleted) {
		t.Fatalf("resend after delete = %v", err)
	}
	if _, err := f.s.ConfirmBotInteraction(sendingItem.ID, 1, "c2"); err != nil {
		t.Fatalf("confirm after delete = %v", err)
	}
	kept, _ := f.s.BotInteraction(ready.ID)
	if kept.ProjectID != "p" || kept.Reply == "" {
		t.Fatalf("record snapshot lost = %+v", kept)
	}
}

func TestBotDeleteMarksInFlightNotificationsUnknown(t *testing.T) {
	f := newDeliveryFixture(t, domain.BotChannelWeCom)
	now := time.Now().UTC()
	for _, d := range []domain.NotificationDelivery{
		{ID: "d-sending", BotID: f.bot.ID, ProjectID: "p", Status: "sending", CreatedAt: now, NextAttemptAt: now},
		{ID: "d-pending", BotID: f.bot.ID, ProjectID: "p", Status: "pending", CreatedAt: now, NextAttemptAt: now},
		{ID: "d-sent", BotID: f.bot.ID, ProjectID: "p", Status: "sent", CreatedAt: now, NextAttemptAt: now},
	} {
		if err := f.s.DB.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.DeleteNotificationBot(f.bot.ID); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"d-sending": "send_unknown", "d-pending": "cancelled", "d-sent": "sent"}
	for id, status := range want {
		d, _ := f.s.NotificationDelivery(id)
		if d.Status != status {
			t.Fatalf("%s = %s, want %s", id, d.Status, status)
		}
	}
}

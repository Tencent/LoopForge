package repo

import (
	"agentflow/internal/domain"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Stage G statements on a real MySQL/MariaDB: push targets and their
// verification, two-phase notification pushes, target switching, the
// expired-reply push resend and pending draft confirmation under
// concurrency. Run with AGENTFLOW_TEST_MYSQL_DSN.
func TestMySQLStageGPushAndDrafts(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_BOT_TEST_MYSQL_DSN")
	if dsn == "" {
		dsn = os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	}
	if dsn == "" {
		t.Skip("MySQL required")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetCredentialKeyring(testCredentialKeyring)
	suffix := fmt.Sprint(time.Now().UnixNano())
	bot := domain.NotificationBot{ID: "g-" + suffix, Channel: domain.BotChannelDingTalk, BotID: "ding-" + suffix, ProjectID: "p-" + suffix, OwnerID: 1, Enabled: true, InteractionEnabled: true, TenantKey: "corp", TenantState: domain.BotTenantLocked}
	if _, err := s.SaveNotificationBotChecked(bot, "secret", nil); err != nil {
		t.Fatal(err)
	}
	bot, _ = s.NotificationBot(bot.ID)
	now := time.Now().UTC()
	token, err := s.NewBotBindingChallenge(bot, "staff", now, true)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.ClaimBotBindingChallenge(token, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmBotUserBinding(bot, "staff", code, now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkBotPushTargetVerified(bot, "staff", 1, now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkBotPushTargetVerified(bot, "staff", 2, now); !errors.Is(err, ErrNotificationTargetUnverified) {
		t.Fatalf("wrong user verified: %v", err)
	}
	targets, err := s.BotPushTargets(bot)
	if err != nil || len(targets) != 1 || targets[0].VerifiedAt == nil {
		t.Fatalf("targets = %+v %v", targets, err)
	}

	// Two-phase push with database time.
	d := domain.NotificationDelivery{ID: "gd-" + suffix, ProjectID: bot.ProjectID, BotID: bot.ID, EventType: "failed", TargetID: "staff", Content: "x", Status: "pending", CreatedAt: now, NextAttemptAt: now.Add(-time.Second)}
	if err := s.CreateNotificationDelivery(d); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&domain.NotificationDelivery{}).Where("id = ?", d.ID).Updates(map[string]any{"status": "sending", "lease_token": "lt"}).Error; err != nil {
		t.Fatal(err)
	}
	d.Status, d.LeaseToken = "sending", "lt"
	begun, elapsed, resumed, err := s.BeginNotificationPush(d)
	if err != nil || resumed || begun.SendKey == "" || begun.SendStartedAtUS == 0 || elapsed != 0 {
		t.Fatalf("begin = %+v %d %v %v", begun, elapsed, resumed, err)
	}
	if again, _, resumed, _ := s.BeginNotificationPush(d); !resumed || again.SendKey != begun.SendKey {
		t.Fatalf("second begin = %+v %v", again, resumed)
	}
	if err := s.FinishNotificationPush(d, NotificationPushResult{Status: "send_unknown", Detail: "未知", Next: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmNotificationReceived(bot.ProjectID, d.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.NotificationDelivery(d.ID); got.Status != "sent" || got.ReceiptSource != "manual" {
		t.Fatalf("confirmed = %+v", got)
	}
	q := domain.NotificationDelivery{ID: "gq-" + suffix, ProjectID: bot.ProjectID, BotID: bot.ID, EventType: "failed", TargetID: "staff", Content: "x", Status: "pending", CreatedAt: now, NextAttemptAt: now.Add(time.Hour)}
	s.CreateNotificationDelivery(q)
	if n, err := s.SwitchNotificationTarget(bot.ID, domain.BotChat{ID: "other", Name: "o", Type: "single"}, "migrate"); err != nil || n != 1 {
		t.Fatalf("migrate = %d %v", n, err)
	}
	if got, _ := s.NotificationDelivery(q.ID); got.TargetID != "other" || got.DeliveryGeneration != 1 {
		t.Fatalf("migrated = %+v", got)
	}

	// Expired reply switched to a push.
	item := domain.BotInteraction{ID: "gi-" + suffix, BotID: bot.ID, ProjectID: bot.ProjectID, SenderID: "staff", Reply: "答复", Status: domain.BotReplyFailed, TerminalReason: "route_expired", DeliveryGeneration: 1, SendKey: "old", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	out, err := s.PushResendBotInteraction(item.ID, 1, 1, "k", "过期")
	if err != nil || out.SendRoute != BotSendRoutePush || out.DeliveryGeneration != 2 {
		t.Fatalf("push resend = %+v %v", out, err)
	}

	// Pending drafts: concurrent confirmations, revocation.
	draft, err := s.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: bot.ID, IdentityScope: bot.IdentityScope(), SenderID: "staff", ProjectID: bot.ProjectID, UserID: 1, Summary: "1. 保存需求：😀", Drafts: `[{"tool":"issues_save","body":{"title":"` + strings.Repeat("长", 3000) + `"}}]`}, now)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := s.BeginBotPendingDraft(draft.ID, 1, now); results <- err }()
	}
	won := 0
	for i := 0; i < 8; i++ {
		if err := <-results; err == nil {
			won++
		} else if !errors.Is(err, ErrBotDraftNotPending) {
			t.Fatal(err)
		}
	}
	if won != 1 {
		t.Fatalf("confirmed %d times", won)
	}
	pending, _ := s.CreateBotPendingDraft(domain.BotPendingDraft{ConfigID: bot.ID, IdentityScope: bot.IdentityScope(), SenderID: "staff", UserID: 1, Drafts: "[]"}, now)
	binding, _ := s.BotUserBinding(bot.IdentityScope(), "staff")
	if err := s.RevokeBotUserBinding(binding.ID, 1, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.BotPendingDraft(pending.ID, now); got.Status != BotDraftCancelled {
		t.Fatalf("after revoke = %+v", got)
	}
	if b, _ := s.UsersRepo.botBindingRow(s.DB, binding.ID); b.TargetVerifiedAt != nil {
		t.Fatal("revocation kept the push verification")
	}
	if err := s.DeleteNotificationBot(bot.ID); err != nil {
		t.Fatal(err)
	}
}

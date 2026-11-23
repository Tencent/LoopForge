package repo

import (
	"agentflow/internal/domain"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// retentionCase seeds rows at known ages and checks one purge run against
// them. It runs on SQLite and, with AGENTFLOW_TEST_MYSQL_DSN, on MySQL.
func retentionCase(t *testing.T, s *Store, suffix string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	id := func(name string) string { return name + "-" + suffix }
	interaction := func(name, status string, age time.Duration) {
		at := now.Add(-age)
		item := domain.BotInteraction{ID: id(name), BotID: "cfg-" + suffix, Content: "问题 " + name, Reply: "答复 " + name, ReplyRoute: "sealed", Status: status, CreatedAt: at, UpdatedAt: at}
		if err := s.DB.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Model(&domain.BotInteraction{}).Where("id = ?", item.ID).UpdateColumn("updated_at", at).Error; err != nil {
			t.Fatal(err)
		}
	}
	interaction("i-fresh", domain.BotReplied, 10*day)
	interaction("i-body", domain.BotReplied, 100*day)
	interaction("i-failed-body", domain.BotReplyFailed, 100*day)
	interaction("i-old", domain.BotCancelled, 200*day)
	interaction("i-unknown", domain.BotReplyUnknown, 400*day)
	interaction("i-ready", domain.BotReady, 400*day)
	interaction("i-running", domain.BotRunning, 400*day)

	delivery := func(name, status string, age time.Duration) {
		at := now.Add(-age)
		d := domain.NotificationDelivery{ID: id(name), ProjectID: "p-" + suffix, BotID: "cfg-" + suffix, EventType: "failed", Content: "通知 " + name, ReportRequest: "{}", Status: status, CreatedAt: at, NextAttemptAt: at}
		if status == "sent" {
			d.SentAt = &at
		}
		if err := s.CreateNotificationDelivery(d); err != nil {
			t.Fatal(err)
		}
	}
	delivery("d-fresh", "sent", 10*day)
	delivery("d-body", "failed", 100*day)
	delivery("d-old", "sent", 200*day)
	delivery("d-unknown", "send_unknown", 400*day)
	delivery("d-pending", "pending", 400*day)
	// Created long ago but retried recently: not finished long ago.
	delivery("d-retried", "failed", 200*day)
	if err := s.DB.Model(&domain.NotificationDelivery{}).Where("id = ?", id("d-retried")).UpdateColumn("next_attempt_at", now.Add(-day)).Error; err != nil {
		t.Fatal(err)
	}

	draft := func(name, status string, age time.Duration) {
		at := now.Add(-age)
		row := botPendingDraftRow{ID: id(name), ConfigID: "cfg-" + suffix, UserID: 1, Summary: "1. 保存需求", Drafts: `[{"tool":"issues_save"}]`, Status: status, ExpiresAt: at.Add(30 * time.Minute), CreatedAt: at, UpdatedAt: at}
		if err := s.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Model(&botPendingDraftRow{}).Where("id = ?", row.ID).UpdateColumn("updated_at", at).Error; err != nil {
			t.Fatal(err)
		}
	}
	draft("g-pending-expired", BotDraftPending, 2*day)
	draft("g-body", BotDraftExecuted, 100*day)
	draft("g-old", BotDraftCancelled, 200*day)
	draft("g-review", BotDraftNeedsReview, 400*day)
	draft("g-live", BotDraftPending, 0)
	if err := s.DB.Model(&botPendingDraftRow{}).Where("id = ?", id("g-live")).UpdateColumn("expires_at", now.Add(10*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}

	native := func(name, status, cleanup string, age time.Duration) {
		at := now.Add(-age)
		row := botNativeSessionRow{ID: id(name), ConfigID: "cfg-" + suffix, UserID: 1, Engine: "codebuddy", RequestedSessionID: "req-" + name + suffix, NativeSessionID: "nat-" + name + suffix, StorageScope: "assistant-cli-home", Status: status, CleanupStatus: cleanup, CreatedAt: at, FinishedAt: &at}
		if err := s.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	native("n-removed-old", "completed", BotNativeCleanupRemoved, 200*day)
	native("n-error-old", "completed", BotNativeCleanupError, 200*day)
	native("n-pending-old", "completed", "", 200*day)

	policy := BotRetentionPolicy{Body: 90 * day, Record: 180 * day, Batch: 2, Batches: 5}
	var res BotRetentionResult
	for _, purge := range []func(context.Context, time.Time, BotRetentionPolicy) (BotRetentionResult, error){s.PurgeBotInteractions, s.PurgeBotNotifications, s.PurgeBotChatRecords} {
		part, err := purge(context.Background(), now, policy)
		if err != nil {
			t.Fatal(err)
		}
		res.Add(part)
	}
	if res.InteractionBodies < 2 || res.Interactions < 1 || res.DeliveryBodies < 1 || res.Deliveries < 1 || res.DraftsExpired < 1 || res.DraftBodies < 1 || res.Drafts < 1 || res.NativeMappings < 1 {
		t.Fatalf("result = %+v", res)
	}

	get := func(name string) (domain.BotInteraction, bool) {
		item, err := s.BotInteraction(id(name))
		return item, err == nil
	}
	if item, ok := get("i-fresh"); !ok || item.Content == "" || item.BodyPurgedAt != nil {
		t.Fatalf("fresh = %+v", item)
	}
	for _, name := range []string{"i-body", "i-failed-body"} {
		item, ok := get(name)
		if !ok || item.Content != "" || item.Reply != "" || item.ReplyRoute != "" || item.BodyPurgedAt == nil || item.Status == "" {
			t.Fatalf("%s = %+v", name, item)
		}
		if !item.UpdatedAt.Before(now.Add(-99 * day)) {
			t.Fatalf("%s: purge moved updated_at to %v", name, item.UpdatedAt)
		}
	}
	if _, ok := get("i-old"); ok {
		t.Fatal("old interaction kept")
	}
	for _, name := range []string{"i-unknown", "i-ready", "i-running"} {
		if item, ok := get(name); !ok || item.Content == "" {
			t.Fatalf("%s must stay untouched: %+v", name, item)
		}
	}
	// A purged failed reply cannot be sent again.
	if _, err := s.ResendBotInteraction(id("i-failed-body"), 1, "k-"+suffix, "x"); !errors.Is(err, ErrBotStateConflict) && !errors.Is(err, ErrBotDeleted) {
		t.Fatalf("resend after purge = %v", err)
	}

	dget := func(name string) (domain.NotificationDelivery, bool) {
		d, err := s.NotificationDelivery(id(name))
		return d, err == nil
	}
	if d, ok := dget("d-fresh"); !ok || d.Content == "" {
		t.Fatalf("fresh delivery = %+v", d)
	}
	if d, ok := dget("d-body"); !ok || d.Content != "" || d.ContentSource != "purged" {
		t.Fatalf("delivery body = %+v", d)
	}
	if err := s.RetryNotification("p-"+suffix, id("d-body")); err == nil {
		t.Fatal("retried a purged notification")
	}
	if _, ok := dget("d-old"); ok {
		t.Fatal("old delivery kept")
	}
	for _, name := range []string{"d-unknown", "d-pending", "d-retried"} {
		if d, ok := dget(name); !ok || d.Content == "" {
			t.Fatalf("%s must stay: %+v", name, d)
		}
	}

	if d, _ := s.BotPendingDraft(id("g-pending-expired"), now); d.Status != BotDraftExpired {
		t.Fatalf("expired pending draft = %+v", d)
	}
	if d, _ := s.BotPendingDraft(id("g-body"), now); d.Drafts != "" || d.Summary != "" || d.Status != BotDraftExecuted {
		t.Fatalf("draft body = %+v", d)
	}
	if _, err := s.BotPendingDraft(id("g-old"), now); err == nil {
		t.Fatal("old draft kept")
	}
	if d, _ := s.BotPendingDraft(id("g-review"), now); d.Drafts == "" {
		t.Fatal("needs_review draft purged")
	}
	if d, _ := s.BotPendingDraft(id("g-live"), now); d.Status != BotDraftPending || d.Drafts == "" {
		t.Fatalf("live draft = %+v", d)
	}

	var left []string
	s.DB.Model(&botNativeSessionRow{}).Where("config_id = ?", "cfg-"+suffix).Order("id").Pluck("id", &left)
	if len(left) != 2 || left[0] != id("n-error-old") || left[1] != id("n-pending-old") {
		t.Fatalf("native mappings left = %v", left)
	}
	due, err := s.DueBotNativeCleanups(now.Add(-90*day), 100)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, d := range due {
		if d.ID == id("n-error-old") || d.ID == id("n-pending-old") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("due = %+v", due)
	}
}

func TestBotRetentionPurge(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	retentionCase(t, s, "sqlite")
}

func TestMySQLBotRetentionPurge(t *testing.T) {
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
	retentionCase(t, s, time.Now().Format("150405.000000"))
}

func TestBotNativeCleanupSkipsSharedSessions(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	old := now.Add(-100 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	for _, row := range []botNativeSessionRow{
		{ID: "a", UserID: 1, RequestedSessionID: "shared-req", NativeSessionID: "nat-a", Status: "completed", FinishedAt: &old},
		{ID: "b", UserID: 1, RequestedSessionID: "shared-req", NativeSessionID: "nat-b", Status: "completed", FinishedAt: &recent},
		{ID: "c", UserID: 1, RequestedSessionID: "own-req", NativeSessionID: "nat-c", Status: "completed", FinishedAt: &old},
		{ID: "d", UserID: 1, RequestedSessionID: "run-req", NativeSessionID: "", Status: "started"},
		{ID: "e", UserID: 1, RequestedSessionID: "run-req", NativeSessionID: "nat-e", Status: "failed", FinishedAt: &old},
	} {
		row.StorageScope = "assistant-cli-home"
		row.CreatedAt = old
		if err := s.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	due, err := s.DueBotNativeCleanups(now.Add(-90*24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != "c" {
		t.Fatalf("due = %+v", due)
	}
	if err := s.RecordBotNativeCleanup(BotNativeCleanupResult{ID: "c", Status: BotNativeCleanupRemoved, Paths: []string{".codebuddy/projects/x/own-req.jsonl"}}, now); err != nil {
		t.Fatal(err)
	}
	if status, paths, _, _ := s.BotNativeCleanupState("c"); status != BotNativeCleanupRemoved || paths != ".codebuddy/projects/x/own-req.jsonl" {
		t.Fatalf("state = %s %s", status, paths)
	}
	// A settled mapping is not overwritten by a late duplicate run.
	s.RecordBotNativeCleanup(BotNativeCleanupResult{ID: "c", Status: BotNativeCleanupError, Detail: "late"}, now)
	if status, _, _, _ := s.BotNativeCleanupState("c"); status != BotNativeCleanupRemoved {
		t.Fatal("settled cleanup overwritten")
	}
}

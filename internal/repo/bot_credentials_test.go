package repo

import (
	"agentflow/internal/credcrypt"
	"agentflow/internal/domain"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testKeyring(t *testing.T, spec string) *credcrypt.Keyring {
	t.Helper()
	parts := []string{}
	for _, kid := range strings.Split(spec, ",") {
		parts = append(parts, kid+":"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat(kid[:1], 32))))
	}
	k, err := credcrypt.Parse(strings.Join(parts, ","))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestBotCredentialSealedLegacyReadableAndReencrypt(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetCredentialKeyring(testKeyring(t, "a1"))
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli_x", ProjectID: "p", OwnerID: 1}
	if err := s.SaveNotificationBot(bot, "app-secret"); err != nil {
		t.Fatal(err)
	}
	var row botCredentialRow
	s.DB.First(&row, "bot_id = ?", "cfg")
	if !strings.HasPrefix(row.Secret, "lf1:a1:") || strings.Contains(row.Secret, "app-secret") {
		t.Fatalf("stored = %q", row.Secret)
	}
	if got, err := s.BotCredential("cfg"); err != nil || got != "app-secret" {
		t.Fatalf("read = %q %v", got, err)
	}
	// A ciphertext copied to another configuration does not open.
	s.DB.Create(&botCredentialRow{BotID: "other", Secret: row.Secret})
	if _, err := s.BotCredential("other"); !errors.Is(err, credcrypt.ErrDecrypt) {
		t.Fatalf("copied ciphertext opened: %v", err)
	}
	s.DB.Where("bot_id = ?", "other").Delete(&botCredentialRow{})
	// Legacy plaintext stays readable and is reported for migration.
	legacy := domain.NotificationBot{ID: "legacy", BotID: "wx", ProjectID: "p", OwnerID: 1}
	if err := s.SaveNotificationBot(legacy, ""); err != nil {
		t.Fatal(err)
	}
	s.DB.Create(&botCredentialRow{BotID: "legacy", Secret: "plain-secret"})
	if got, _ := s.BotCredential("legacy"); got != "plain-secret" {
		t.Fatalf("legacy read = %q", got)
	}
	if b, _ := s.NotificationBot("legacy"); b.CredentialFormat != "plaintext" {
		t.Fatalf("legacy format = %q", b.CredentialFormat)
	}
	// Rotation: new key first, old key still readable; reencrypt is re-entrant.
	s.SetCredentialKeyring(testKeyring(t, "b2,a1"))
	report, err := s.ReencryptBotCredentials()
	if err != nil || report.Rewrapped != 2 || report.Plaintext != 0 || report.ByKID["b2"] != 2 {
		t.Fatalf("reencrypt = %+v %v", report, err)
	}
	again, err := s.ReencryptBotCredentials()
	if err != nil || again.Rewrapped != 0 || again.Skipped != 2 {
		t.Fatalf("second run = %+v %v", again, err)
	}
	for id, want := range map[string]string{"cfg": "app-secret", "legacy": "plain-secret"} {
		if got, err := s.BotCredential(id); err != nil || got != want {
			t.Fatalf("%s after rotation = %q %v", id, got, err)
		}
	}
	if strings.Contains(report.String(), "secret") {
		t.Fatal("report printed a value")
	}
	// Without a key new secrets are refused and nothing is written.
	s.SetCredentialKeyring(nil)
	bot2 := bot
	bot2.ID, bot2.BotID = "cfg2", "cli_y"
	if err := s.SaveNotificationBot(bot2, "s"); !errors.Is(err, ErrCredentialKeyMissing) {
		t.Fatalf("missing key = %v", err)
	}
	if _, err := s.NotificationBot("cfg2"); err == nil {
		t.Fatal("configuration saved without its credential")
	}
	if _, err := s.BotCredential("cfg"); !errors.Is(err, credcrypt.ErrKeyMissing) {
		t.Fatalf("sealed value without keyring = %v", err)
	}
}

func TestBotConfigVersionsCASAndImmutableFields(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli_x", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	saved, err := s.SaveNotificationBotChecked(bot, "s1", nil)
	if err != nil || saved.ConfigVersion != 1 || saved.CredentialVersion != 1 || saved.TenantState != domain.BotTenantAwaiting {
		t.Fatalf("create = %+v %v", saved, err)
	}
	stale := int64(0)
	if _, err := s.SaveNotificationBotChecked(saved, "", &stale); !errors.Is(err, ErrBotConfigChanged) {
		t.Fatalf("stale version = %v", err)
	}
	saved.Name = "renamed"
	v := saved.ConfigVersion
	renamed, err := s.SaveNotificationBotChecked(saved, "", &v)
	if err != nil || renamed.ConfigVersion != 2 || renamed.CredentialVersion != 1 || renamed.ConnectionFingerprint() != saved.ConnectionFingerprint() {
		t.Fatalf("rename = %+v %v", renamed, err)
	}
	rotated, err := s.SaveNotificationBotChecked(renamed, "s2", nil)
	if err != nil || rotated.CredentialVersion != 2 || rotated.ConnectionFingerprint() == renamed.ConnectionFingerprint() {
		t.Fatalf("rotate = %+v %v", rotated, err)
	}
	moved := rotated
	moved.BotID = "cli_other"
	if _, err := s.SaveNotificationBotChecked(moved, "", nil); !errors.Is(err, ErrBotImmutableField) {
		t.Fatalf("app change = %v", err)
	}
	switched := rotated
	switched.Channel = domain.BotChannelWeCom
	if _, err := s.SaveNotificationBotChecked(switched, "", nil); !errors.Is(err, ErrBotImmutableField) {
		t.Fatalf("channel change = %v", err)
	}
	// The same Feishu app and a WeCom Bot with the same external ID do not
	// collide: receive slots include the channel.
	wecom := domain.NotificationBot{ID: "wx", BotID: "cli_x", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	if err := s.SaveNotificationBot(wecom, "s"); err != nil {
		t.Fatalf("cross-channel slot = %v", err)
	}
	dup := bot
	dup.ID = "cfg-dup"
	if err := s.SaveNotificationBot(dup, "s"); !errors.Is(err, ErrInteractiveBotAlreadyExists) {
		t.Fatalf("same app twice = %v", err)
	}
}

func TestBotTenantCandidateAndConfirmation(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli_x", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	saved, _ := s.SaveNotificationBotChecked(bot, "s1", nil)
	if _, err := s.ConfirmBotTenant("cfg", "t1", saved.ConfigVersion, 9); !errors.Is(err, ErrBotTenantCandidate) {
		t.Fatalf("confirm before discovery = %v", err)
	}
	state, err := s.RecordBotTenantCandidate("cfg", "t1", "evt-1", saved.CredentialVersion)
	if err != nil || state != domain.BotTenantAwaiting {
		t.Fatal(state, err)
	}
	after, _ := s.NotificationBot("cfg")
	if after.ConfigVersion != saved.ConfigVersion || after.TenantCandidate == nil || after.TenantCandidate.TenantKey != "t1" {
		t.Fatalf("candidate changed the version or was lost: %+v", after)
	}
	if _, err := s.ConfirmBotTenant("cfg", "t2", after.ConfigVersion, 9); !errors.Is(err, ErrBotTenantCandidate) {
		t.Fatalf("confirm of a different key = %v", err)
	}
	locked, err := s.ConfirmBotTenant("cfg", "t1", after.ConfigVersion, 9)
	if err != nil || locked.TenantState != domain.BotTenantLocked || locked.ConfigVersion != after.ConfigVersion+1 || locked.TenantConfirmedBy != 9 {
		t.Fatalf("confirm = %+v %v", locked, err)
	}
	stored, _ := s.NotificationBot("cfg")
	if stored.TenantKey != "t1" || stored.ConnectionFingerprint() != saved.ConnectionFingerprint() {
		t.Fatalf("locked = %+v", stored)
	}
	// Rotating the secret keeps the lock.
	rotated, _ := s.SaveNotificationBotChecked(stored, "s2", nil)
	if rotated.TenantState != domain.BotTenantLocked || rotated.TenantKey != "t1" {
		t.Fatalf("rotation dropped the lock: %+v", rotated)
	}
	// A second, different candidate on an unlocked configuration is a conflict.
	other := domain.NotificationBot{ID: "cfg2", Channel: domain.BotChannelFeishu, BotID: "cli_y", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	o, _ := s.SaveNotificationBotChecked(other, "s", nil)
	s.RecordBotTenantCandidate("cfg2", "t1", "e1", o.CredentialVersion)
	if state, _ := s.RecordBotTenantCandidate("cfg2", "t9", "e2", o.CredentialVersion); state != domain.BotTenantConflict {
		t.Fatalf("conflict = %s", state)
	}
	if reset, err := s.RediscoverBotTenant("cfg2", o.ConfigVersion); err != nil || reset.TenantState != domain.BotTenantAwaiting || reset.TenantCandidate != nil {
		t.Fatalf("rediscover = %+v %v", reset, err)
	}
}

func TestDingTalkExpectedTenantAndSealedRoute(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bot := domain.NotificationBot{ID: "dt", Channel: domain.BotChannelDingTalk, BotID: "ding-app", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true, TenantExpected: "corp-a", TenantExpectedBy: 5}
	saved, err := s.SaveNotificationBotChecked(bot, "secret", nil)
	if err != nil || saved.TenantState != domain.BotTenantAwaiting || saved.TenantExpected != "corp-a" {
		t.Fatalf("create = %+v %v", saved, err)
	}
	// A different enterprise first: conflict, and it stays a conflict even
	// when the expected enterprise shows up afterwards.
	if state, _ := s.RecordBotTenantCandidate("dt", "corp-x", "e1", saved.CredentialVersion); state != domain.BotTenantConflict {
		t.Fatalf("foreign first = %s", state)
	}
	if state, _ := s.RecordBotTenantCandidate("dt", "corp-a", "e2", saved.CredentialVersion); state != domain.BotTenantConflict {
		t.Fatalf("conflict must be sticky, got %s", state)
	}
	conflicted, _ := s.NotificationBot("dt")
	if conflicted.TenantCandidate == nil || conflicted.TenantCandidate.Conflict != "corp-x" || conflicted.TenantKey != "" {
		t.Fatalf("conflict = %+v", conflicted)
	}
	// The administrator corrects the CorpId: the check starts afresh.
	conflicted.TenantExpected, conflicted.TenantExpectedBy = "corp-x", 6
	corrected, err := s.SaveNotificationBotChecked(conflicted, "", nil)
	if err != nil || corrected.TenantState != domain.BotTenantAwaiting || corrected.TenantCandidate != nil || corrected.TenantExpected != "corp-x" {
		t.Fatalf("correct = %+v %v", corrected, err)
	}
	if state, _ := s.RecordBotTenantCandidate("dt", "corp-x", "e3", corrected.CredentialVersion); state != domain.BotTenantLocked {
		t.Fatalf("match = %s", state)
	}
	locked, _ := s.NotificationBot("dt")
	if locked.TenantKey != "corp-x" || locked.TenantConfirmedBy != 6 || locked.ConfigVersion != corrected.ConfigVersion+1 || locked.ConnectionFingerprint() != corrected.ConnectionFingerprint() {
		t.Fatalf("locked = %+v", locked)
	}
	// Once locked the expected value can no longer be changed.
	locked.TenantExpected = "corp-y"
	after, _ := s.SaveNotificationBotChecked(locked, "", nil)
	if after.TenantKey != "corp-x" || after.TenantExpected != "corp-x" || after.TenantState != domain.BotTenantLocked {
		t.Fatalf("locked change = %+v", after)
	}
	// Session webhooks are sealed per interaction.
	sealed, err := s.SealBotRoute("dt", "i1", "https://oapi.dingtalk.com/robot/sendBySession?session=x")
	if err != nil || !strings.HasPrefix(sealed, "lf1:") || strings.Contains(sealed, "session") {
		t.Fatalf("seal = %q %v", sealed, err)
	}
	if got, err := s.OpenBotRoute("dt", "i1", sealed); err != nil || !strings.HasSuffix(got, "session=x") {
		t.Fatalf("open = %q %v", got, err)
	}
	if _, err := s.OpenBotRoute("dt", "i2", sealed); err == nil {
		t.Fatal("route opened for another interaction")
	}
	if got, _ := s.OpenBotRoute("dt", "i1", "om_plain"); got != "om_plain" {
		t.Fatalf("plain route = %q", got)
	}
	s.SetCredentialKeyring(nil)
	if _, err := s.SealBotRoute("dt", "i1", "x"); !errors.Is(err, ErrCredentialKeyMissing) {
		t.Fatalf("no keyring = %v", err)
	}
}

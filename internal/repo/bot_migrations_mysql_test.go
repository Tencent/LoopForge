package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// execSQLScript runs a migration script statement by statement (the scripts
// contain no semicolons inside literals) and returns the rows of the last
// SELECT for inspection.
func execSQLScript(t *testing.T, s *Store, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		lines = append(lines, line)
	}
	for _, stmt := range strings.Split(strings.Join(lines, "\n"), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := s.DB.Exec(stmt).Error; err != nil {
			t.Fatalf("%s: %v\n%s", path, err, stmt)
		}
	}
}

// Legacy rows (payload only, physical columns at their added defaults) are
// backfilled by 036 and receive their WeCom ownership from 037; both scripts
// can run twice.
func TestMySQLMigrations036And037BackfillLegacyRows(t *testing.T) {
	s := openBotMySQL(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	legacyID, sharedA, sharedB := "legacy-"+suffix, "shared-a-"+suffix, "shared-b-"+suffix
	ext, sharedExt := "wx-"+suffix, "wx-shared-"+suffix
	payload := func(id, botID string, enabled, interactive bool) []byte {
		return []byte(fmt.Sprintf(`{"id":%q,"projectId":"p-%s","name":"n","botId":%q,"enabled":%t,"interactionEnabled":%t}`, id, suffix, botID, enabled, interactive))
	}
	for _, row := range []notificationBotRow{
		{ID: legacyID, ProjectID: "p-" + suffix, Payload: payload(legacyID, ext, true, true)},
		{ID: sharedA, ProjectID: "p-" + suffix, Payload: payload(sharedA, sharedExt, true, false)},
		{ID: sharedB, ProjectID: "p-" + suffix, Payload: payload(sharedB, sharedExt, false, false)},
	} {
		if err := s.DB.Exec("INSERT INTO notification_bots (id, project_id, payload) VALUES (?, ?, ?)", row.ID, row.ProjectID, string(row.Payload)).Error; err != nil {
			t.Fatal(err)
		}
	}
	s.DB.Create(&botCredentialRow{BotID: legacyID, Secret: "legacy-plain"})
	interaction := domain.BotInteraction{ID: "mi-" + suffix, BotID: legacyID, Status: "replied", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	s.DB.Create(&interaction)
	bound := domain.BotUserBinding{ID: botIdentityID(ext, "u1"), BotID: ext, SenderID: "u1", UserID: 7, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	ambiguous := domain.BotUserBinding{ID: botIdentityID(sharedExt, "u2"), BotID: sharedExt, SenderID: "u2", UserID: 8, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	s.DB.Create(&bound)
	s.DB.Create(&ambiguous)
	t.Cleanup(func() {
		s.DB.Where("id IN ?", []string{legacyID, sharedA, sharedB}).Delete(&notificationBotRow{})
		s.DB.Where("bot_id = ?", legacyID).Delete(&botCredentialRow{})
		s.DB.Delete(&interaction)
		s.DB.Where("id IN ?", []string{bound.ID, ambiguous.ID}).Delete(&domain.BotUserBinding{})
	})
	if bots, _ := s.InteractiveNotificationBots(); containsBot(bots, legacyID) {
		t.Fatal("unmigrated row must not receive before 036")
	}
	for i := 0; i < 2; i++ {
		execSQLScript(t, s, "../../migrations/036_enterprise_im_channels.sql")
		execSQLScript(t, s, "../../migrations/037_bot_binding_ownership.sql")
	}
	var row notificationBotRow
	s.DB.First(&row, "id = ?", legacyID)
	legacy, _ := row.bot()
	if row.Channel != "wecom" || row.ExternalAppID != ext || !row.Enabled || !row.InteractionEnabled || row.ConfigVersion != 1 || row.CredentialVersion != 1 || row.ReceiveSlot == nil || *row.ReceiveSlot != legacy.ReceiveSlot() {
		t.Fatalf("backfill = %+v slot=%v", row, row.ReceiveSlot)
	}
	if bots, _ := s.InteractiveNotificationBots(); !containsBot(bots, legacyID) {
		t.Fatal("migrated row does not receive")
	}
	if got, _ := s.BotInteraction(interaction.ID); got.ProjectID != "p-"+suffix {
		t.Fatalf("project snapshot = %q", got.ProjectID)
	}
	var b1, b2 domain.BotUserBinding
	s.DB.First(&b1, "id = ?", bound.ID)
	s.DB.First(&b2, "id = ?", ambiguous.ID)
	if b1.ConfigID != legacyID || b1.Channel != "wecom" || b2.ConfigID != "" {
		t.Fatalf("ownership = %+v / %+v", b1, b2)
	}
	if secret, err := s.BotCredential(legacyID); err != nil || secret != "legacy-plain" {
		t.Fatalf("legacy credential = %q %v", secret, err)
	}
}

func containsBot(bots []domain.NotificationBot, id string) bool {
	for _, b := range bots {
		if b.ID == id {
			return true
		}
	}
	return false
}

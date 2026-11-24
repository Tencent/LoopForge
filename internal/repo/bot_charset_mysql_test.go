package repo

import (
	"agentflow/internal/domain"
	"errors"
	"github.com/go-sql-driver/mysql"
	"os"
	"strings"
	"testing"
)

// This DSN must point to a disposable local MySQL database: the test changes
// this table's charset to reproduce an existing utf8mb3 deployment.
func TestBotCharsetMySQLMigration(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_BOT_CHARSET_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable local Docker MySQL only")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var charset string
	query := "SELECT character_set_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='bot_interactions' AND column_name='reply'"
	store.DB.Raw(query).Scan(&charset)
	if charset != "utf8mb4" {
		t.Fatal("new table inherited utf8mb3", charset)
	}
	bot := domain.NotificationBot{ID: "emoji-bot", BotID: "emoji-external", ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
	store.SetCredentialKeyring(testCredentialKeyring)
	if err := store.SaveNotificationBot(bot, "s"); err != nil {
		t.Fatal(err)
	}
	defer store.DeleteNotificationBot(bot.ID)
	lease, _, err := store.ClaimBotConnection(bot.LeaseKey(), "emoji-owner")
	if err != nil {
		t.Fatal(err)
	}
	item := domain.BotInteraction{ID: "emoji-regression", Status: "running", BotID: bot.ID, LeaseOwner: lease.Owner, LeaseEpoch: lease.Epoch}
	if err := store.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	defer store.DB.Delete(&item)
	if err := store.DB.Exec("ALTER TABLE bot_interactions CONVERT TO CHARACTER SET utf8mb3 COLLATE utf8mb3_general_ci").Error; err != nil {
		t.Fatal(err)
	}
	err = store.SaveBotReply(item, lease, "你好 👋", "")
	var sqlErr *mysql.MySQLError
	if !errors.As(err, &sqlErr) || (sqlErr.Number != 1366 && sqlErr.Number != 3988) {
		t.Fatal("utf8mb3 failure not reproduced", err)
	}
	script, err := os.ReadFile("../../migrations/009_bot_interactions_utf8mb4.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Exec(string(script)).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBotReply(item, lease, "你好 👋", ""); err != nil {
		t.Fatal(err)
	}
	saved, err := store.BotInteraction(item.ID)
	if err != nil || saved.Status != "ready" || !strings.Contains(saved.Reply, "👋") {
		t.Fatal(saved, err)
	}
}

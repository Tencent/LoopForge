package botinteraction

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestEmojiPersistenceFailureFinishesWithActionableReply(t *testing.T) {
	for _, code := range []uint16{1366, 3988} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			store, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			item := domain.BotInteraction{ID: "emoji-reply", BotID: "bot", Status: "running"}
			if err := store.DB.Create(&item).Error; err != nil {
				t.Fatal(err)
			}
			failures := 0
			if err := store.DB.Callback().Update().Before("gorm:update").Register("test_utf8mb3", func(tx *gorm.DB) {
				updates, ok := tx.Statement.Dest.(map[string]any)
				if !ok {
					return
				}
				if text, ok := updates["reply"].(string); ok && strings.Contains(text, "👋") {
					failures++
					tx.AddError(&mysql.MySQLError{Number: code, Message: "Incorrect string value"})
				}
			}); err != nil {
				t.Fatal(err)
			}
			service := New(store)
			reply, detail, err := service.saveReply(item, "你好 👋", "")
			if err != nil || failures != 1 || !strings.Contains(reply, "utf8mb4") || !strings.Contains(detail, fmt.Sprint(code)) {
				t.Fatal(reply, detail, err, failures)
			}
			saved, err := store.BotInteraction(item.ID)
			if err != nil || saved.Status != "ready" || saved.Reply != reply || saved.LastError != detail {
				t.Fatal(saved, err)
			}

		})
	}
}

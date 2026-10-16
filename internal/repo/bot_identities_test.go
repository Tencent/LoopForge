package repo

import (
	"agentflow/internal/domain"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestBotBindingRequiresBothIdentitiesAndRejectsReplay(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	token, err := s.NewBotBindingChallenge("bot", "sender", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BotUserBinding("bot", "sender"); err == nil {
		t.Fatal("link immediately bound")
	}
	code, err := s.ClaimBotBindingChallenge(token, 42, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ClaimBotBindingChallenge(token, 42, now.Add(time.Minute))
	if err != nil || again != code {
		t.Fatal("reopening rotated code", err)
	}
	if _, err = s.ClaimBotBindingChallenge(token, 43, now); err == nil {
		t.Fatal("other account replaced claim")
	}
	if _, err = s.ConfirmBotUserBinding("bot", "other-sender", code, now); err == nil {
		t.Fatal("group participant stole binding")
	}
	if _, err = s.ConfirmBotUserBinding("other-bot", "sender", code, now); err == nil {
		t.Fatal("cross-Bot confirmation")
	}
	var challenge domain.BotBindingChallenge
	s.DB.First(&challenge, "id = ?", botBindingHash(token))
	if challenge.CodeHash == code || challenge.ID == token {
		t.Fatal("plaintext challenge stored")
	}
	var wg sync.WaitGroup
	success := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ConfirmBotUserBinding("bot", "sender", code, now)
			success <- err == nil
		}()
	}
	wg.Wait()
	close(success)
	count := 0
	for ok := range success {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatal("confirmation count", count)
	}
	binding, err := s.BotUserBinding("bot", "sender")
	if err != nil || binding.UserID != 42 {
		t.Fatal(binding, err)
	}
	if err = s.RevokeBotUserBinding(binding.ID, 43, now); err == nil {
		t.Fatal("other user revoked binding")
	}
	if err = s.RevokeBotUserBinding(binding.ID, 42, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConfirmBotUserBinding("bot", "sender", code, now); err == nil {
		t.Fatal("old code rebound after revoke")
	}
	if _, err = s.BotBindingChallengeStatus(token, 42, now); err == nil {
		t.Fatal("old link survived revoke")
	}
}
func TestBotBindingExpiryAttemptLimitAndLatestLink(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	old, _ := s.NewBotBindingChallenge("bot", "sender", now)
	token, _ := s.NewBotBindingChallenge("bot", "sender", now.Add(time.Second))
	if _, err = s.ClaimBotBindingChallenge(old, 42, now); err == nil {
		t.Fatal("old link remains usable")
	}
	code, err := s.ClaimBotBindingChallenge(token, 42, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, claimErr := s.ClaimBotBindingChallenge(token, 42, now)
		if claimErr != nil || again != code {
			t.Fatal("reopening changed code or reset attempts", claimErr)
		}
		if _, err = s.ConfirmBotUserBinding("bot", "sender", "wrong", now); err == nil {
			t.Fatal("invalid code accepted")
		}
	}
	if _, err = s.ConfirmBotUserBinding("bot", "sender", code, now); err == nil {
		t.Fatal("attempt limit ignored")
	}
	token, _ = s.NewBotBindingChallenge("bot", "sender", now)
	code, _ = s.ClaimBotBindingChallenge(token, 42, now)
	if _, err = s.ConfirmBotUserBinding("bot", "sender", code, now.Add(16*time.Minute)); err == nil {
		t.Fatal("expired code accepted")
	}
}

func TestBotBindingMySQLConcurrentConfirmation(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_BOT_BINDING_TEST_DSN")
	if dsn == "" {
		t.Skip("local Docker MySQL integration only")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	bot := fmt.Sprintf("binding-test-%d", now.UnixNano())
	sender := "sender"
	id := botIdentityID(bot, sender)
	defer s.DB.Where("binding_id = ?", id).Delete(&domain.BotBindingChallenge{})
	defer s.DB.Where("id = ?", id).Delete(&domain.BotUserBinding{})
	token, err := s.NewBotBindingChallenge(bot, sender, now)
	if err != nil {
		t.Fatal(err)
	}
	code, err := s.ClaimBotBindingChallenge(token, 9999, now)
	if err != nil {
		t.Fatal(err)
	}
	var claims sync.WaitGroup
	claimResults := make(chan bool, 12)
	for i := 0; i < 12; i++ {
		claims.Add(1)
		go func() {
			defer claims.Done()
			again, err := s.ClaimBotBindingChallenge(token, 9999, now)
			claimResults <- err == nil && again == code
		}()
	}
	claims.Wait()
	close(claimResults)
	for same := range claimResults {
		if !same {
			t.Fatal("MySQL concurrent claim rotated code")
		}
	}
	var wg sync.WaitGroup
	results := make(chan bool, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ConfirmBotUserBinding(bot, sender, code, now)
			results <- err == nil
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("MySQL duplicate confirmation", winners)
	}

	expired := domain.BotBindingChallenge{ID: botBindingHash(bot + "-old"), BindingID: id, Status: "expired", ExpiresAt: now.Add(-25 * time.Hour)}
	if err := s.DB.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteExpiredBotBindingChallenges(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var count int64
	s.DB.Model(&domain.BotBindingChallenge{}).Where("id = ?", expired.ID).Count(&count)
	if count != 0 {
		t.Fatal("MySQL expired request survived")
	}
	if _, err := s.BotUserBinding(bot, sender); err != nil {
		t.Fatal("MySQL cleanup removed binding", err)
	}
	if status, err := s.BotBindingChallengeStatus(token, 9999, now); err != nil || status != "confirmed" {
		t.Fatal("MySQL cleanup removed recent request", status, err)
	}
}

func TestBotBindingChallengeCleanupRetentionAndIsolation(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	statuses := []string{"pending", "claimed", "confirmed", "expired"}
	for _, status := range statuses {
		if err := s.DB.Create(&domain.BotBindingChallenge{ID: "old-" + status, Status: status, ExpiresAt: now.Add(-25 * time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []domain.BotBindingChallenge{
		{ID: "active", Status: "claimed", ExpiresAt: now.Add(time.Minute)},
		{ID: "recent-expired", Status: "expired", ExpiresAt: now.Add(-23 * time.Hour)},
		{ID: "recent-confirmed", Status: "confirmed", ExpiresAt: now.Add(-time.Minute)},
	} {
		if err := s.DB.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	binding := domain.BotUserBinding{ID: "permanent", BotID: "bot", SenderID: "sender", UserID: 42}
	if err := s.DB.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	removed, err := s.DeleteExpiredBotBindingChallenges(context.Background(), now)
	if err != nil || removed != 4 {
		t.Fatal(removed, err)
	}
	var remaining int64
	s.DB.Model(&domain.BotBindingChallenge{}).Count(&remaining)
	if remaining != 3 {
		t.Fatal("active/recent requests deleted", remaining)
	}
	var stored domain.BotUserBinding
	if err := s.DB.First(&stored, "id = ?", binding.ID).Error; err != nil || stored.UserID != 42 {
		t.Fatal("permanent binding changed", err)
	}
	removed, err = s.DeleteExpiredBotBindingChallenges(context.Background(), now)
	if err != nil || removed != 0 {
		t.Fatal("cleanup not idempotent", removed, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.DeleteExpiredBotBindingChallenges(cancelled, now); err == nil {
		t.Fatal("cancelled cleanup ignored context")
	}
}

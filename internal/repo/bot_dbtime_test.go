package repo

import (
	"agentflow/internal/domain"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// The database clock expression must agree with real time within a small
// tolerance on both dialects, and stored *_us values must compare as integers.
func TestDBNowUSMatchesWallClockOnSQLite(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checkDBClock(t, s)
}

func TestDBNowUSMatchesWallClockOnMySQL(t *testing.T) {
	s := openBotMySQL(t)
	checkDBClock(t, s)
	// SET timestamp skews NOW(6) for this session: the lease follows the
	// database clock, not the API host clock.
	sqlDB, _ := s.DB.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := s.DB.Exec("SET timestamp = ?", float64(time.Now().Add(-time.Hour).UnixMicro())/1e6).Error; err != nil {
		t.Fatal(err)
	}
	defer s.DB.Exec("SET timestamp = DEFAULT")
	now, err := s.dbNowUS(s.DB)
	if err != nil || time.Since(time.UnixMicro(now)) < 59*time.Minute {
		t.Fatalf("skewed clock = %v %v", time.UnixMicro(now), err)
	}
}

func checkDBClock(t *testing.T, s *Store) {
	t.Helper()
	before := time.Now().UnixMicro()
	now, err := s.dbNowUS(s.DB)
	after := time.Now().UnixMicro()
	if err != nil || now < before-2_000_000 || now > after+2_000_000 {
		t.Fatalf("db clock %d outside [%d,%d]: %v", now, before, after, err)
	}
	// Integer comparison across second boundaries: a value written one
	// microsecond ago is in the past, one written a second ahead is not.
	key := fmt.Sprintf("clock-%d", time.Now().UnixNano())
	defer s.DB.Where("bot_id = ?", key).Delete(&domain.BotConnection{})
	if err := s.DB.Create(&domain.BotConnection{BotID: key, LeaseUntil: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&domain.BotConnection{}).Where("bot_id = ?", key).Update("lease_until_us", s.nowUSExpr(1_000_000)).Error; err != nil {
		t.Fatal(err)
	}
	var live int64
	s.DB.Model(&domain.BotConnection{}).Where("bot_id = ? AND lease_until_us > "+s.dbNowUSSQL(), key).Count(&live)
	if live != 1 {
		t.Fatal("future lease not live")
	}
	s.DB.Model(&domain.BotConnection{}).Where("bot_id = ?", key).Update("lease_until_us", s.nowUSExpr(-1))
	s.DB.Model(&domain.BotConnection{}).Where("bot_id = ? AND lease_until_us > "+s.dbNowUSSQL(), key).Count(&live)
	if live != 0 {
		t.Fatal("past lease still live")
	}
}

func openBotMySQL(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	s.SetCredentialKeyring(testCredentialKeyring)
	t.Cleanup(s.Close)
	return s
}

// Concurrent saves of the same receiving application leave exactly one
// enabled receiver (unique receive_slot plus the definition lock).
func TestMySQLConcurrentSavesKeepOneReceiver(t *testing.T) {
	s := openBotMySQL(t)
	app := fmt.Sprintf("cli_race_%d", time.Now().UnixNano())
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = s.SaveNotificationBot(domain.NotificationBot{ID: fmt.Sprintf("%s-%d", app, i), Channel: domain.BotChannelFeishu, BotID: app, ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}, "s")
		}(i)
	}
	wg.Wait()
	ok := 0
	for i, err := range results {
		defer s.DeleteNotificationBot(fmt.Sprintf("%s-%d", app, i))
		if err == nil {
			ok++
		} else if !errors.Is(err, ErrInteractiveBotAlreadyExists) {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if ok != 1 {
		t.Fatalf("enabled receivers = %d", ok)
	}
}

// Deleting a Bot while its worker records a send result must not deadlock:
// both follow config -> connection -> interaction. Whichever commits first
// wins; the other sees a state conflict.
func TestMySQLDeleteRacesSendResultWithoutDeadlock(t *testing.T) {
	s := openBotMySQL(t)
	for round := 0; round < 10; round++ {
		id := fmt.Sprintf("race-%d-%d", time.Now().UnixNano(), round)
		bot := domain.NotificationBot{ID: id, BotID: "ext-" + id, ProjectID: "p", OwnerID: 1, Enabled: true, InteractionEnabled: true}
		if err := s.SaveNotificationBot(bot, "s"); err != nil {
			t.Fatal(err)
		}
		lease, _, err := s.ClaimBotConnection(bot.LeaseKey(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		item := domain.BotInteraction{ID: BotMessageID(bot.IdentityScope(), "m"), BotID: id, SenderID: "u", Content: "c"}
		s.EnqueueBotInteraction(item, "single", "u")
		claimed, err := s.ClaimBotInteraction(id, lease, BotSendPolicy{})
		if err != nil || claimed == nil {
			t.Fatal(claimed, err)
		}
		if err := s.SaveBotReply(*claimed, lease, "r", ""); err != nil {
			t.Fatal(err)
		}
		s.ClaimBotInteraction(id, lease, BotSendPolicy{})
		if _, err := s.BeginBotSend(item.ID, lease, BotSendPolicy{}); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var delErr, finErr error
		wg.Add(2)
		go func() { defer wg.Done(); delErr = s.DeleteNotificationBot(id) }()
		go func() {
			defer wg.Done()
			_, finErr = s.FinishBotSend(item.ID, lease, BotSendPolicy{}, BotSendOutcome{Outcome: "delivered"})
		}()
		wg.Wait()
		if delErr != nil {
			t.Fatalf("round %d delete: %v", round, delErr)
		}
		got, _ := s.BotInteraction(item.ID)
		switch {
		case finErr == nil && got.Status == domain.BotReplied:
		case errors.Is(finErr, ErrBotLeaseLost) && got.Status == domain.BotReplyUnknown && got.TerminalReason == "bot_deleted_during_send":
		default:
			t.Fatalf("round %d: finish=%v status=%s reason=%s", round, finErr, got.Status, got.TerminalReason)
		}
		s.DB.Where("bot_id = ?", id).Delete(&domain.BotInteraction{})
		s.DB.Where("bot_id = ?", id).Delete(&domain.BotConversation{})
		s.DB.Where("bot_id = ?", bot.LeaseKey()).Delete(&domain.BotConnection{})
	}
}

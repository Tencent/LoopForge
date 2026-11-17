package repo

// Characterization tests for stored WeCom identifiers (enterprise IM plan
// R1-1). Existing rows were written with these derivations; new channels must
// not change them for WeCom.

import (
	"agentflow/internal/domain"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func baselineSHA(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func TestWeComBaselineKeyDerivations(t *testing.T) {
	if got := BotMessageID("ext-bot", "msg-1"); got != baselineSHA("ext-bot", "msg-1") {
		t.Fatalf("BotMessageID = %s", got)
	}
	if got := BotConversationID("cfg-1", "group", "chat-1"); got != baselineSHA("cfg-1", "group", "chat-1") {
		t.Fatalf("BotConversationID = %s", got)
	}
	if got := botIdentityID("ext-bot", "alice"); got != baselineSHA("ext-bot", "alice") {
		t.Fatalf("botIdentityID = %s", got)
	}
}

func TestWeComBaselineBindingAndLeaseUseExternalBotID(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	if _, err := s.NewBotBindingChallenge("ext-bot", "alice", now); err != nil {
		t.Fatal(err)
	}
	var row domain.BotUserBinding
	if err := s.DB.First(&row, "id = ?", baselineSHA("ext-bot", "alice")).Error; err != nil {
		t.Fatalf("binding row keyed by external bot id: %v", err)
	}
	if row.BotID != "ext-bot" || row.SenderID != "alice" {
		t.Fatalf("binding row: %+v", row)
	}
	ok, err := s.ClaimBotConnection("ext-bot", "owner-a", now)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	var conn domain.BotConnection
	if err := s.DB.First(&conn, "bot_id = ?", "ext-bot").Error; err != nil || conn.Owner != "owner-a" {
		t.Fatalf("lease row keyed by external bot id: %+v %v", conn, err)
	}
	if ok, _ := s.ClaimBotConnection("ext-bot", "owner-b", now.Add(10*time.Second)); ok {
		t.Fatal("second owner claimed a live lease")
	}
	if ok, _ := s.ClaimBotConnection("ext-bot", "owner-b", now.Add(46*time.Second)); !ok {
		t.Fatal("second owner could not take over an expired lease")
	}
}

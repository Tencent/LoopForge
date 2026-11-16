package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func legacySHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestHexSHA256KnownVectors(t *testing.T) {
	cases := map[string]string{
		"":    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"abc": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
	}
	for in, want := range cases {
		if got := hexSHA256(in); got != want {
			t.Fatalf("hexSHA256(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestHashCallersKeepLegacyOutput(t *testing.T) {
	if got, want := BotConversationID("bot", "group", "chat"), legacySHA256Hex("bot\x00group\x00chat"); got != want {
		t.Fatalf("BotConversationID = %s, want %s", got, want)
	}
	if got, want := BotMessageID("bot", "msg"), legacySHA256Hex("bot\x00msg"); got != want {
		t.Fatalf("BotMessageID = %s, want %s", got, want)
	}
	if got, want := botIdentityID("bot", "sender"), legacySHA256Hex("bot\x00sender"); got != want {
		t.Fatalf("botIdentityID = %s, want %s", got, want)
	}
	long := strings.Repeat("r", 80)
	if got, want := integrationLockName(long), "agentflow:integrate:"+legacySHA256Hex(long)[:40]; got != want {
		t.Fatalf("integrationLockName(long) = %s, want %s", got, want)
	}
	if got := integrationLockName("repo-1"); got != "agentflow:integrate:repo-1" {
		t.Fatalf("integrationLockName(short) = %s", got)
	}
}

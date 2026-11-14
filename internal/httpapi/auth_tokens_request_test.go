package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestNormalizeAPITokenRequest(t *testing.T) {
	cases := []struct {
		name     string
		rawName  string
		days     int
		wantName string
		wantDays int
		problem  string
	}{
		{name: "trims name and defaults to 90 days", rawName: "  ci  ", days: 0, wantName: "ci", wantDays: 90},
		{name: "accepts lower bound", rawName: "ci", days: 1, wantName: "ci", wantDays: 1},
		{name: "accepts upper bound", rawName: "ci", days: 365, wantName: "ci", wantDays: 365},
		{name: "accepts 128 runes", rawName: strings.Repeat("令", 128), days: 30, wantName: strings.Repeat("令", 128), wantDays: 30},
		{name: "rejects blank name", rawName: "   ", days: 30, problem: "Token 名称不能为空"},
		{name: "rejects name over 128 runes", rawName: strings.Repeat("令", 129), days: 30, problem: "Token 名称不能超过 128 个字符"},
		{name: "rejects negative days", rawName: "ci", days: -1, problem: "有效期必须在 1 到 365 天之间"},
		{name: "rejects days over a year", rawName: "ci", days: 366, problem: "有效期必须在 1 到 365 天之间"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, days, problem := normalizeAPITokenRequest(tc.rawName, tc.days)
			if problem != tc.problem {
				t.Fatalf("problem = %q, want %q", problem, tc.problem)
			}
			if problem != "" {
				return
			}
			if name != tc.wantName || days != tc.wantDays {
				t.Fatalf("got (%q, %d), want (%q, %d)", name, days, tc.wantName, tc.wantDays)
			}
		})
	}
}

func TestNewAPITokenRecord(t *testing.T) {
	now := time.Date(2026, 11, 14, 23, 50, 0, 0, time.UTC)
	plain := "afp_abcdefghijklmnopqrstuvwxyz"
	token := newAPITokenRecord(42, "ci", plain, now, 7)

	if !strings.HasPrefix(token.ID, "tok-") || len(token.ID) <= len("tok-") {
		t.Fatalf("ID = %q, want tok-<random>", token.ID)
	}
	if token.UserID != 42 || token.Name != "ci" {
		t.Fatalf("owner/name = (%d, %q), want (42, ci)", token.UserID, token.Name)
	}
	if token.Prefix != plain[:12] {
		t.Fatalf("Prefix = %q, want %q", token.Prefix, plain[:12])
	}
	sum := sha256.Sum256([]byte(plain))
	if token.TokenHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("TokenHash = %q, want sha256 of plaintext", token.TokenHash)
	}
	if strings.Contains(token.TokenHash, plain) || token.Prefix == plain {
		t.Fatal("record must not keep the full plaintext token")
	}
	if !token.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", token.CreatedAt, now)
	}
	if token.ExpiresAt == nil || !token.ExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("ExpiresAt = %v, want %v", token.ExpiresAt, now.Add(7*24*time.Hour))
	}
}

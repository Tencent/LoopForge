package credcrypt

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func TestParseValidatesFormat(t *testing.T) {
	if k, err := Parse("  "); k != nil || err != nil {
		t.Fatalf("empty = %v %v", k, err)
	}
	for _, bad := range []string{"nokey", "k1:" + base64.StdEncoding.EncodeToString([]byte("short")), "bad kid!:" + key(1), "k1:" + key(1) + ",k1:" + key(2), "k1:%%%"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	k, err := Parse("new:" + key(2) + ", old:" + key(1))
	if err != nil || k.CurrentKID() != "new" {
		t.Fatalf("parse = %v %v", k, err)
	}
}

func TestSealOpenRoundTripAndBinding(t *testing.T) {
	k, _ := Parse("k1:" + key(1))
	sealed, err := k.Seal([]byte(`{"secret":"s3cr3t"}`), "bot-1|feishu")
	if err != nil || !strings.HasPrefix(sealed, "lf1:k1:") || strings.Contains(sealed, "s3cr3t") {
		t.Fatalf("seal = %q %v", sealed, err)
	}
	again, _ := k.Seal([]byte(`{"secret":"s3cr3t"}`), "bot-1|feishu")
	if again == sealed {
		t.Fatal("nonce reused")
	}
	plain, err := k.Open(sealed, "bot-1|feishu")
	if err != nil || string(plain) != `{"secret":"s3cr3t"}` {
		t.Fatalf("open = %q %v", plain, err)
	}
	if _, err := k.Open(sealed, "bot-2|feishu"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("other owner opened: %v", err)
	}
	tampered := sealed[:len(sealed)-4] + "AAAA"
	if _, err := k.Open(tampered, "bot-1|feishu"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered opened: %v", err)
	}
	if _, err := k.Open("lf1:k1:not-base64", "bot-1|feishu"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("malformed opened: %v", err)
	}
	if _, err := k.Open("plain", "x"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("plaintext opened as sealed: %v", err)
	}
}

func TestRotationAndMissingKeys(t *testing.T) {
	old, _ := Parse("k1:" + key(1))
	sealed, _ := old.Seal([]byte("v"), "a")
	rotated, _ := Parse("k2:" + key(2) + ",k1:" + key(1))
	if plain, err := rotated.Open(sealed, "a"); err != nil || string(plain) != "v" {
		t.Fatalf("old key not readable after rotation: %q %v", plain, err)
	}
	if !rotated.NeedsRewrap(sealed) || rotated.NeedsRewrap(mustSeal(t, rotated, "a")) || !rotated.NeedsRewrap("legacy") || rotated.NeedsRewrap("") {
		t.Fatal("rewrap classification")
	}
	dropped, _ := Parse("k2:" + key(2))
	if _, err := dropped.Open(sealed, "a"); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("missing kid = %v", err)
	}
	var none *Keyring
	if _, err := none.Seal([]byte("v"), "a"); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("nil seal = %v", err)
	}
	if _, err := none.Open(sealed, "a"); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("nil open = %v", err)
	}
	if KID(sealed) != "k1" || KID("plain") != "" || !IsSealed(sealed) {
		t.Fatal("kid helpers")
	}
}

func mustSeal(t *testing.T, k *Keyring, aad string) string {
	t.Helper()
	v, err := k.Seal([]byte("x"), aad)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

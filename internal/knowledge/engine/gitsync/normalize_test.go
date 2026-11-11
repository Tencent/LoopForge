package gitsync

import "testing"

func TestNormalizePrivateKeyAddsTrailingNewline(t *testing.T) {
	const key = "fake-openssh-key-material"
	if got := normalizePrivateKey(key); got != key+"\n" {
		t.Fatalf("normalized key = %q", got)
	}
	if got := normalizePrivateKey(key + "\n"); got != key+"\n" {
		t.Fatalf("already terminated key changed: %q", got)
	}
	if got := normalizePrivateKey(""); got != "" {
		t.Fatalf("empty key changed: %q", got)
	}
}

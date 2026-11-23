package knowledge

import (
	"strings"
	"testing"
)

func TestConsoleSecretRoundTrip(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "unit-test-key")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "")

	plain := `{"account":"ops@example.com","password":"p@ss word!"}`
	sealed, err := sealConsoleSecret("wk-1", plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !strings.HasPrefix(sealed, consoleSecretPrefix) {
		t.Fatalf("expected %q prefix, got %q", consoleSecretPrefix, sealed)
	}
	if strings.Contains(sealed, "p@ss word!") {
		t.Fatal("ciphertext must not contain the plaintext")
	}

	opened, err := openConsoleSecret("wk-1", sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened != plain {
		t.Fatalf("round trip mismatch: %q", opened)
	}

	// 随机 nonce：同一明文两次加密的密文必须不同。
	again, err := sealConsoleSecret("wk-1", plain)
	if err != nil {
		t.Fatalf("seal again: %v", err)
	}
	if again == sealed {
		t.Fatal("expected a fresh nonce for every seal")
	}
}

func TestConsoleSecretBindsConnection(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "unit-test-key")

	sealed, err := sealConsoleSecret("wk-1", "secret-value")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// 密文被复制到另一行时 AAD 不匹配，必须解不开。
	if _, err := openConsoleSecret("wk-2", sealed); err == nil {
		t.Fatal("a secret must not open under another connection id")
	}
	if _, err := openConsoleSecret("", sealed); err == nil {
		t.Fatal("a secret must not open without its connection id")
	}
	if opened, err := openConsoleSecret("wk-1", sealed); err != nil || opened != "secret-value" {
		t.Fatalf("the owning connection must still open it: %q %v", opened, err)
	}
}

func TestConsoleSecretRejectsTampering(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "unit-test-key")

	sealed, err := sealConsoleSecret("wk-1", "secret-value")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	body := strings.TrimPrefix(sealed, consoleSecretPrefix)
	idx := len(body) / 2
	flip := byte('A')
	if body[idx] == 'A' {
		flip = 'B'
	}
	tampered := consoleSecretPrefix + body[:idx] + string(flip) + body[idx+1:]
	if _, err := openConsoleSecret("wk-1", tampered); err == nil {
		t.Fatal("tampered ciphertext must not decrypt")
	}

	// 截断的密文同样不能通过校验。
	short := consoleSecretPrefix + body[:len(body)/2]
	if _, err := openConsoleSecret("wk-1", short); err == nil {
		t.Fatal("truncated ciphertext must not decrypt")
	}
}

func TestConsoleSecretRejectsRotatedKey(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "first-key")
	sealed, err := sealConsoleSecret("wk-1", "secret-value")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "second-key")
	if _, err := openConsoleSecret("wk-1", sealed); err == nil {
		t.Fatal("a rotated deployment key must not decrypt old secrets")
	}

	// 专用密钥缺失时回退到会话密钥；材料不同，旧密文不可解。
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "another-key")
	if _, err := openConsoleSecret("wk-1", sealed); err == nil {
		t.Fatal("fallback key material must not decrypt a secret sealed with the dedicated key")
	}
}

func TestConsoleSecretRejectsUnknownFormat(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "unit-test-key")

	cases := []string{
		"",
		"   ",
		"plaintext",
		"v2:" + "AAAA",
		consoleSecretPrefix + "not base64!!",
		consoleSecretPrefix + "AAAA",
	}
	for _, in := range cases {
		if _, err := openConsoleSecret("wk-1", in); err == nil {
			t.Fatalf("input %q must be rejected", in)
		}
	}
}

func TestConsoleSecretRequiresKeyMaterial(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "")

	if _, err := sealConsoleSecret("wk-1", "secret-value"); err == nil {
		t.Fatal("sealing must fail without deployment key material")
	}
	if _, err := sealConsoleSecret("wk-1", "   "); err == nil {
		t.Fatal("an empty password must be refused")
	}
}

func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("invalid password: hunter2 (hunter2)", "hunter2", "")
	if strings.Contains(got, "hunter2") {
		t.Fatalf("secret must be redacted: %q", got)
	}
	if !strings.Contains(got, "invalid password") {
		t.Fatalf("non-secret context should survive: %q", got)
	}
}

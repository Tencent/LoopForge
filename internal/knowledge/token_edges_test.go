package knowledge

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestSignProxyTokenExternalRefWithoutUpstreamToken(t *testing.T) {
	// An external HTTP knowledge MCP has its own credentials, so the shared
	// knowledge settings may carry no upstream token. Signing must still work
	// as long as a session secret is available to derive the signing key.
	t.Setenv("AGENTFLOW_SESSION_SECRET", "session-secret-value")
	t.Setenv("SESSION_SECRET", "")

	settings := domain.KnowledgeSettings{Enabled: true, URL: ""}
	now := time.Unix(1_900_000_000, 0)
	ref := &domain.KnowledgeRef{ServerID: "handbook", Mode: "all"}
	claims := ProxyClaims{ProjectID: "p9", UserID: 3, Ref: ref, RunID: "r1", NodeID: "n1", Attempt: 2}

	token, err := SignProxyToken(settings, claims, now)
	if err != nil {
		t.Fatalf("external ref signing must not require upstream token: %v", err)
	}
	got, err := VerifyProxyToken(settings, token, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "r1" || got.NodeID != "n1" || got.Attempt != 2 || got.Ref == nil || got.Ref.ServerID != "handbook" {
		t.Fatalf("verified claims lost binding data: %+v", got)
	}

	// Team knowledge ref still requires the upstream token.
	if _, err := SignProxyToken(settings, ProxyClaims{Ref: &domain.KnowledgeRef{ServerID: domain.KnowledgeMCPServerID, Mode: "all"}}, now); err != ErrNotConfigured {
		t.Fatalf("team ref without upstream token error = %v, want ErrNotConfigured", err)
	}
}

func TestSignProxyTokenExplicitExpiry(t *testing.T) {
	settings := domain.KnowledgeSettings{Token: "upstream-x"}
	now := time.Unix(2_000_000_000, 0)
	expiry := now.Add(10 * time.Minute).Unix()
	token, err := SignProxyToken(settings, ProxyClaims{ProjectID: "p", ExpiresAt: expiry}, now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyProxyToken(settings, token, now.Add(time.Minute))
	if err != nil || claims.ExpiresAt != expiry {
		t.Fatalf("explicit expiry not preserved: claims=%+v err=%v", claims, err)
	}
	if _, err := VerifyProxyToken(settings, token, now.Add(11*time.Minute)); err == nil {
		t.Fatal("token accepted after explicit expiry")
	}
}

func TestVerifyProxyTokenRejectsGarbageAndWrongSecret(t *testing.T) {
	settings := domain.KnowledgeSettings{Token: "upstream-y"}
	now := time.Unix(2_100_000_000, 0)
	for _, bad := range []string{"", "not-a-token", proxyTokenPrefix + "@@@", "afkb9.abc.def"} {
		if _, err := VerifyProxyToken(settings, bad, now); err == nil {
			t.Fatalf("garbage token %q accepted", bad)
		}
	}

	token, err := SignProxyToken(settings, ProxyClaims{ProjectID: "p"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, proxyTokenPrefix) {
		t.Fatalf("token missing prefix: %q", token)
	}
	rotated := settings
	rotated.Token = "upstream-z"
	if _, err := VerifyProxyToken(rotated, token, now); err == nil {
		t.Fatal("token verified with rotated signing key")
	}
}

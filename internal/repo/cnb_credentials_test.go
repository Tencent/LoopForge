package repo

import (
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

func TestCNBTokenStoreRoundTrip(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	const token = "cnb-fake-token-0000"
	if got := s.GitHTTPSCredentials(1); len(got) != 0 {
		t.Fatalf("a user without a token has credentials: %+v", got)
	}
	if err := s.SetCNBToken(1, " "+token+" "); err != nil {
		t.Fatal(err)
	}
	got := s.GitHTTPSCredentials(1)
	if len(got) != 1 || got[0] != gitwork.CNBCredential(token) {
		t.Fatalf("credentials = %+v", got)
	}
	user, _ := s.User(1)
	public, _ := json.Marshal(domain.PublicUser(user))
	if strings.Contains(string(public), token) || !strings.Contains(string(public), `"cnbTokenConfigured":true`) {
		t.Fatalf("PublicUser must report configured and hide the token: %s", public)
	}
	full, _ := json.Marshal(user)
	if strings.Contains(string(full), token) {
		t.Fatalf("User JSON exposes the token: %s", full)
	}
	// Saving another credential and a login refresh must keep the token.
	if _, err := s.UpdateUserCredentials(1, UserCredentialUpdate{CodexAPIKey: "codex-fake"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(domain.User{ID: 1, LoginName: user.LoginName}); err != nil {
		t.Fatal(err)
	}
	if got := s.GitHTTPSCredentials(1); len(got) != 1 {
		t.Fatalf("token lost after unrelated writes: %+v", got)
	}
	if err := s.SetCNBToken(1, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.GitHTTPSCredentials(1); len(got) != 0 {
		t.Fatalf("cleared token still returned: %+v", got)
	}
}

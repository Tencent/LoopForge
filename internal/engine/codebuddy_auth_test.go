package engine

import (
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestResolveCodeBuddyAPIKeyFromUserOnly(t *testing.T) {
	t.Setenv(codeBuddyAPIKeyEnv, "ck_host")
	t.Setenv(codeBuddyInternetEnv, "ioa")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 21, LoginName: "owner", CodeBuddyAPIKey: "ck_user", CodeBuddyInternetEnvironment: "private-net"}); err != nil {
		t.Fatal(err)
	}
	if got := resolveCodeBuddyAPIKey(s, 21); got != "ck_user" {
		t.Fatalf("user key = %q", got)
	}
	if got := resolveCodeBuddyInternet(s, 21); got != "private-net" {
		t.Fatalf("user internet = %q", got)
	}
	if got := resolveCodeBuddyCredential(s, 21); got.APIKey != "ck_user" || got.InternetEnvironment != "private-net" {
		t.Fatalf("typed user credential = %+v", got)
	}
	if got := resolveCodeBuddyAPIKey(s, 999); got != "" {
		t.Fatalf("missing user must not use host env, got %q", got)
	}
	if got := resolveCodeBuddyInternet(s, 999); got != "" {
		t.Fatalf("missing internet must not use host env, got %q", got)
	}
	if got := resolveCodeBuddyAPIKey(nil, 21); got != "" {
		t.Fatalf("nil store = %q", got)
	}
}

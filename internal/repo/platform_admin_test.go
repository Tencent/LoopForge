package repo

import (
	"testing"

	"agentflow/internal/domain"
)

func TestBuiltinPlatformAdminAllowlist(t *testing.T) {
	if len(BuiltinPlatformAdmins) != 0 {
		t.Fatal("public builds must not privilege named people")
	}
	if IsBuiltinPlatformAdmin("example-admin") || IsBuiltinPlatformAdmin("alice") {
		t.Fatal("no builtin public admins")
	}
}

func TestStoreIsPlatformAdminUsesAllowlistAndRole(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-admin")
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if !s.IsPlatformAdmin("example-admin") {
		t.Fatal("allowlist login must be admin even before a user row exists")
	}
	if s.IsPlatformAdmin("alice") {
		t.Fatal("unknown user must not be admin")
	}
	if err := s.PutUser(domain.User{LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	if s.IsPlatformAdmin("alice") {
		t.Fatal("regular user must not be admin")
	}
	alice, _ := s.UserByLoginName("alice")
	if err := s.SetUserPlatformRole(alice.ID, domain.PlatformRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if !s.IsPlatformAdmin("alice") {
		t.Fatal("explicit platform_role=admin must be honored")
	}
	if err := s.PutUser(domain.User{LoginName: "example-admin", ChineseName: "Robert"}); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.UserByLoginName("example-admin")
	if !ok || stored.PlatformRole != domain.PlatformRoleAdmin {
		t.Fatalf("login refresh must stamp builtin admin role: %+v", stored)
	}
}

func TestEnsurePlatformAdminsDoesNotInsertInTests(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, ok := s.UserByLoginName("example-admin"); ok {
		t.Fatal("sqlite test store must not materialize placeholder admin users")
	}
}

func TestEnvPlatformAdminsAreConfigurable(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "alicewang, boblee, example-admin")
	if !IsAllowlistedPlatformAdmin("alicewang") || !IsAllowlistedPlatformAdmin("BobLee") {
		t.Fatal("env allowlist must authorize extra logins")
	}
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if !s.IsPlatformAdmin("alicewang") {
		t.Fatal("store must honor env platform admins")
	}
	granted, err := s.GrantPlatformAdmin("carol")
	if err != nil || granted.LoginName != "carol" || !granted.Revocable {
		t.Fatalf("grant = %+v err=%v", granted, err)
	}
	if !s.IsPlatformAdmin("carol") {
		t.Fatal("granted login must be admin")
	}
	if err := s.RevokePlatformAdmin("carol"); err != nil {
		t.Fatal(err)
	}
	if s.IsPlatformAdmin("carol") {
		t.Fatal("revoked login must lose admin")
	}
	if err := s.RevokePlatformAdmin("example-admin"); err == nil {
		t.Fatal("builtin admin must not be revocable")
	}
}

package repo

import (
	"errors"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestSharedModelKeysAreStoredPerCLI(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	t.Setenv(CodeBuddySponsorLoginEnv, "")
	if err := st.PutUser(domain.User{ID: 21, LoginName: "owner"}); err != nil {
		t.Fatal(err)
	}
	for _, update := range []PlatformModelKeyUpdate{
		{Provider: domain.SharedModelProviderCodeBuddy, APIKey: "ck_shared", InternetEnvironment: "internal", Models: []string{"glm-5.1"}},
		{Provider: domain.SharedModelProviderCodex, APIKey: "sk_codex", BaseURL: "https://gateway.example.com/v1", Models: []string{"gpt-6.1-terra", "gpt-6.1-sol"}},
		{Provider: domain.SharedModelProviderClaude, APIKey: "sk_claude", Models: []string{"claude-opus-5"}},
	} {
		if err := st.SetPlatformModelKey(update, 21); err != nil {
			t.Fatalf("%s: %v", update.Provider, err)
		}
	}
	keys := st.SharedModelKeys()
	if len(keys) != 3 {
		t.Fatalf("shared keys = %+v", keys)
	}
	if key := keys[domain.SharedModelProviderCodex]; key.APIKey != "sk_codex" || key.BaseURL != "https://gateway.example.com/v1" || strings.Join(key.Models, ",") != "gpt-6.1-terra,gpt-6.1-sol" {
		t.Fatalf("codex key = %+v", key)
	}
	if key := keys[domain.SharedModelProviderClaude]; key.APIKey != "sk_claude" || key.BaseURL != "" {
		t.Fatalf("claude key = %+v", key)
	}
	if key := keys[domain.SharedModelProviderCodeBuddy]; key.InternetEnvironment != "internal" {
		t.Fatalf("codebuddy key = %+v", key)
	}
	if got, ok := keys[domain.SharedModelProviderCodex].Model("gpt-6.1-sol"); !ok || got != "gpt-6.1-sol" {
		t.Fatalf("allowed model = %q %v", got, ok)
	}
	if _, ok := keys[domain.SharedModelProviderCodex].Model("gpt-5.5"); ok {
		t.Fatal("a model outside the allow list must be refused")
	}

	status := st.PlatformModelKeyStatus(domain.SharedModelProviderCodex)
	if !status.Configured || status.UpdatedBy != "owner" || status.BaseURL != "https://gateway.example.com/v1" {
		t.Fatalf("codex status = %+v", status)
	}
	if statuses := st.PlatformModelKeyStatuses(); len(statuses) != len(domain.SharedModelProviders) || statuses[0].Provider != domain.SharedModelProviderCodeBuddy {
		t.Fatalf("statuses = %+v", statuses)
	}

	if err := st.SetPlatformModelKey(PlatformModelKeyUpdate{Provider: "kimi", APIKey: "k", Models: []string{"kimi-k3"}}, 21); !errors.Is(err, ErrSharedModelKeyProvider) {
		t.Fatalf("unsupported provider: %v", err)
	}
	if err := st.SetPlatformModelKey(PlatformModelKeyUpdate{Provider: domain.SharedModelProviderClaude, Models: []string{"claude-opus-5"}}, 21); err != nil {
		t.Fatalf("a models-only update keeps the key: %v", err)
	}
	if key, _ := st.SharedModelKey(domain.SharedModelProviderClaude); key.APIKey != "sk_claude" {
		t.Fatalf("claude key after models-only update = %+v", key)
	}

	if err := st.ClearPlatformModelKey(domain.SharedModelProviderCodex); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.SharedModelKey(domain.SharedModelProviderCodex); ok {
		t.Fatal("cleared Codex key must not be lent")
	}
	if _, ok := st.SharedModelKey(domain.SharedModelProviderCodeBuddy); !ok {
		t.Fatal("clearing one CLI must keep the others")
	}
}

// A deployment upgraded from the CodeBuddy-only table keeps lending that key,
// and clearing it removes both rows so the older key cannot resurface.
func TestLegacyCodeBuddyKeyMovesToThePerCLITable(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	t.Setenv(CodeBuddySponsorLoginEnv, "")
	if err := st.PutUser(domain.User{ID: 21, LoginName: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.Create(&platformCodeBuddyKeyRow{ID: platformCodeBuddyKeyID, APIKey: "ck_legacy", InternetEnvironment: "ioa", Models: `["glm-5.1"]`}).Error; err != nil {
		t.Fatal(err)
	}
	key, ok := st.SharedModelKey(domain.SharedModelProviderCodeBuddy)
	if !ok || key.APIKey != "ck_legacy" || key.InternetEnvironment != "ioa" || strings.Join(key.Models, ",") != "glm-5.1" {
		t.Fatalf("legacy key = %+v", key)
	}
	if status := st.PlatformModelKeyStatus(domain.SharedModelProviderCodeBuddy); !status.Configured {
		t.Fatalf("legacy status = %+v", status)
	}

	// Editing only the models must not ask the administrator to type the key
	// again.
	if err := st.SetPlatformModelKey(PlatformModelKeyUpdate{
		Provider: domain.SharedModelProviderCodeBuddy, InternetEnvironment: "ioa", Models: []string{"glm-5.1", "kimi-k2.6"},
	}, 21); err != nil {
		t.Fatalf("models-only update over the legacy row: %v", err)
	}
	if key, _ := st.SharedModelKey(domain.SharedModelProviderCodeBuddy); key.APIKey != "ck_legacy" || strings.Join(key.Models, ",") != "glm-5.1,kimi-k2.6" {
		t.Fatalf("moved key = %+v", key)
	}
	if legacy, ok := st.legacyPlatformCodeBuddyKey(); !ok || legacy.APIKey != "ck_legacy" {
		t.Fatal("the legacy row must not be rewritten in code")
	}

	if err := st.ClearPlatformCodeBuddyKey(); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.SharedModelKey(domain.SharedModelProviderCodeBuddy); ok {
		t.Fatal("clearing must remove the legacy row too")
	}
}

// A stored key only follows the gateway it was entered for: moving it to
// another Base URL needs the key typed again.
func TestSharedModelKeyBaseURLChangeNeedsTheKey(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	save := func(key, baseURL string) error {
		return s.SetPlatformModelKey(PlatformModelKeyUpdate{Provider: domain.SharedModelProviderClaude, APIKey: key, BaseURL: baseURL, Models: []string{"m"}}, 0)
	}
	if err := save("sk_one", "https://gateway.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := save("", "https://gateway.example.com/"); err != nil {
		t.Fatalf("same gateway, models only: %v", err)
	}
	for _, baseURL := range []string{"https://collector.example.net", ""} {
		if err := save("", baseURL); !errors.Is(err, ErrSharedModelKeyBaseURLChanged) {
			t.Fatalf("move to %q without key: %v", baseURL, err)
		}
	}
	if key, _ := s.SharedModelKey(domain.SharedModelProviderClaude); key.APIKey != "sk_one" || key.BaseURL != "https://gateway.example.com/" {
		t.Fatalf("stored key = %+v", key)
	}
	if err := save("sk_two", ""); err != nil {
		t.Fatalf("move with key: %v", err)
	}
}

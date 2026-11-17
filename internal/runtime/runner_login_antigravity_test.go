package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const agyOriginalLogin = `{"token":{"access_token":"old-access","refresh_token":"original-refresh","expiry":"2026-11-01T00:00:00Z"},"auth_method":"consumer","id_token_email":"owner@example.test"}`
const agyRefreshedLogin = `{"token":{"access_token":"new-access","refresh_token":"new-refresh","expiry":"2026-12-01T00:00:00Z"},"auth_method":"consumer","id_token_email":"owner@example.test"}`

func agyFileLoginFixture(t *testing.T) string {
	t.Helper()
	host := t.TempDir()
	t.Setenv("HOME", host)
	t.Setenv("SSH_CONNECTION", "test-remote-session")
	path := filepath.Join(host, ".gemini", "antigravity-cli", antigravityTokenFile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(agyOriginalLogin), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAntigravityRunnerLoginLifecycle(t *testing.T) {
	source := agyFileLoginFixture(t)
	root := t.TempDir()
	login, _ := runnerLoginSpec(AgentCLIAntigravity)
	settings := filepath.Join(root, "home", login.dir, "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"modelProvider":"gemini"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cleanup, env, err := prepareConversationRunnerLogin(root, login, true)
	if err != nil {
		t.Fatal(err)
	}
	if env["SSH_CONNECTION"] == "" {
		t.Fatal("sandbox did not select official file authentication")
	}
	for _, key := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "AGY_ADC_AUTH", "AGY_LLM_GATEWAY_API_KEY"} {
		if value, present := env[key]; !present || value != "" {
			t.Fatalf("host credential %s was not cleared", key)
		}
	}
	if raw, err := os.ReadFile(settings); err != nil || string(raw) != "{}" {
		t.Fatal("previous API billing mode was retained")
	}
	copy := filepath.Join(root, "home", login.dir, login.file)
	if info, err := os.Stat(copy); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("login copy must be private")
	}
	if err := os.WriteFile(copy, []byte(agyRefreshedLogin), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(source); err != nil || string(raw) != agyRefreshedLogin {
		t.Fatal("refreshed login was not saved to its source")
	}
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatal("isolated credential copy survived cleanup")
	}
	cleanup, _, err = prepareConversationRunnerLogin(root, login, true)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(copy); string(raw) != agyRefreshedLogin {
		t.Fatal("next invocation did not use the refreshed source")
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copy, []byte(agyOriginalLogin), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareConversationRunnerLogin(root, login, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatal("API-key mode retained an account credential")
	}
	if err := os.WriteFile(copy, []byte(agyOriginalLogin), 0600); err != nil {
		t.Fatal(err)
	}
	_, keyEnv, err := ApplyAgentCLICredential(AgentCLIAntigravity, ProviderCredential{APIKey: "personal-key"}, AgentCLIOptions{Model: "gemini-test"}, HostAgentCLIHome(filepath.Join(root, "home")), filepath.Join(root, "home"), []string{"agy", "-p", "test"})
	if err != nil || keyEnv["GEMINI_API_KEY"] != "personal-key" {
		t.Fatalf("API-key configuration failed: %v", err)
	}
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatal("API-key binding retained a credential from an interrupted account-login run")
	}
}

func TestAntigravityKeyringLoginRefresh(t *testing.T) {
	for _, tc := range []struct {
		name, concurrent, refreshed string
		wantWrite, wantError        bool
	}{
		{name: "refresh", refreshed: agyRefreshedLogin, wantWrite: true},
		{name: "host switched accounts", concurrent: "different-host-login", refreshed: agyRefreshedLogin},
		{name: "host logged out", concurrent: "missing", refreshed: agyRefreshedLogin},
		{name: "sandbox switched accounts", refreshed: strings.ReplaceAll(agyRefreshedLogin, "owner@example.test", "other@example.test"), wantError: true},
		{name: "unchanged", refreshed: agyOriginalLogin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored, writes := agyOriginalLogin, 0
			read := func() (string, error) {
				if stored == "missing" {
					return "", os.ErrNotExist
				}
				return stored, nil
			}
			write := func(value string) error { stored = value; writes++; return nil }
			_, keep, err := antigravityKeyringLogin(read, write)
			if err != nil {
				t.Fatal(err)
			}
			if tc.concurrent != "" {
				stored = tc.concurrent
			}
			err = keep([]byte(tc.refreshed))
			if (err != nil) != tc.wantError || (writes == 1) != tc.wantWrite {
				t.Fatalf("writes=%d error=%v", writes, err)
			}
		})
	}
	t.Run("credential errors do not expose secrets", func(t *testing.T) {
		_, _, err := antigravityKeyringLogin(func() (string, error) { return "", errors.New("private-keychain-diagnostic") }, func(string) error { return nil })
		if err == nil || strings.Contains(err.Error(), "private-keychain-diagnostic") {
			t.Fatal("raw keychain error exposed")
		}
	})
}

func TestAntigravityLoginIdentityMigration(t *testing.T) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"owner@example.test"}`))
	var original map[string]any
	if err := json.Unmarshal([]byte(agyOriginalLogin), &original); err != nil {
		t.Fatal(err)
	}
	delete(original, "id_token_email")
	original["id_token"] = "header." + claims + ".signature"
	raw, _ := json.Marshal(original)
	if !sameAntigravityLogin(raw, []byte(agyRefreshedLogin)) {
		t.Fatal("official id_token to id_token_email migration rejected")
	}
	if sameAntigravityLogin(raw, []byte(strings.ReplaceAll(agyRefreshedLogin, "owner@example.test", "other@example.test"))) {
		t.Fatal("another account accepted")
	}
}

func TestAntigravityAccountModelsSaveRefreshedLogin(t *testing.T) {
	source := agyFileLoginFixture(t)
	bin := filepath.Join(t.TempDir(), "agy")
	script := `#!/bin/sh
set -eu
test "$1" = models
test -n "$SSH_CONNECTION"
test -z "${GEMINI_API_KEY:-}${GOOGLE_API_KEY:-}${AGY_ADC_AUTH:-}"
test -f "$HOME/.gemini/antigravity-cli/antigravity-oauth-token"
printf '%s' '` + agyRefreshedLogin + `' > "$HOME/.gemini/antigravity-cli/antigravity-oauth-token"
printf 'gemini-4-hydrogen-high\tGemini 4 Hydrogen (High)\n'
printf 'gemini-4-hydrogen-high\tDuplicate\n'
printf 'Fetching available models...\n' >&2
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	models, err := DiscoverRunnerLoginModels(context.Background(), AgentCLIAntigravity, bin)
	if err != nil || len(models) != 1 || models[0].ID != "gemini-4-hydrogen-high" {
		t.Fatalf("models=%+v error=%v", models, err)
	}
	if raw, _ := os.ReadFile(source); string(raw) != agyRefreshedLogin {
		t.Fatal("model query lost its refreshed credential")
	}
}

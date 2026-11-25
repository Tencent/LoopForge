package runtime

// A sandbox edit is not a trusted issuer response. Legitimate refreshes are
// tested through the manager, independently of these injection attempts.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var writebackProbeProviders = []string{"codex", "claude-code", "agy"}

func probeCredential(provider, account string, tokens probeTokens) []byte {
	var doc map[string]any
	switch provider {
	case "codex":
		idToken := tokens.ID
		if idToken == "" {
			idToken = "fake-id-token"
		}
		doc = map[string]any{
			"auth_mode": "chatgpt", "last_refresh": "2026-11-17T00:00:00Z",
			"tokens": map[string]any{"account_id": account, "id_token": idToken,
				"access_token": tokens.Access, "refresh_token": tokens.Refresh},
		}
	case "claude-code":
		doc = map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": tokens.Access, "refreshToken": tokens.Refresh,
			"expiresAt": tokens.Expires * 1000, "scopes": []string{"user:profile", "user:inference"},
			"subscriptionType": "fake-plan", "rateLimitTier": "fake-tier",
		}}
	case "agy":
		doc = map[string]any{"auth_method": "consumer", "id_token_email": account + "@example.test",
			"token": map[string]any{"access_token": tokens.Access, "refresh_token": tokens.Refresh,
				"expiry": "2026-12-01T00:00:00Z", "token_type": "Bearer"}}
	default:
		panic("unknown test provider")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestRunnerLoginSandboxEditsNotWrittenBack(t *testing.T) {
	for _, provider := range writebackProbeProviders {
		t.Run(provider, func(t *testing.T) {
			original := probeCredential(provider, "account-a", probeTokens{Access: "fake-access-a", Refresh: "fake-refresh-a"})
			cases := []struct {
				name, metadata, access, refresh string
				accepted                        bool
			}{
				{"edited token pair", "account-a", "fake-renewed-a", "fake-rotated-a", false},
				{"whole account swap", "account-b", "fake-access-b", "fake-refresh-b", false},
				{"swap with forged local identity", "account-a", "fake-access-b", "fake-refresh-b", false},
				{"refresh token only", "account-a", "fake-access-a", "fake-refresh-b", false},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					// Override every relevant credential source before preparing a login.
					// No native keychain or real user HOME can be consulted.
					home := t.TempDir()
					t.Setenv("HOME", home)
					t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
					t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
					t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
					t.Setenv("SSH_CONNECTION", "127.0.0.1 1 127.0.0.1 2")
					login, ok := runnerLoginSpec(provider)
					if !ok {
						t.Fatal("missing test provider")
					}
					source := filepath.Join(home, login.dir, login.file)
					if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(source, original, 0600); err != nil {
						t.Fatal(err)
					}
					workspace := t.TempDir()
					cleanup, _, err := prepareConversationRunnerLogin(workspace, login, true)
					if err != nil {
						t.Fatal(err)
					}
					candidate := probeCredential(provider, tc.metadata, probeTokens{Access: tc.access, Refresh: tc.refresh})
					copyPath := filepath.Join(workspace, "home", login.dir, login.file)
					if err := os.WriteFile(copyPath, candidate, 0600); err != nil {
						t.Fatal(err)
					}
					if err := cleanup(); err != nil {
						t.Fatal(err)
					}
					got, err := os.ReadFile(source)
					if err != nil {
						t.Fatal(err)
					}
					accepted := bytes.Equal(got, candidate)
					if accepted != tc.accepted {
						t.Fatalf("sandbox edit accepted = %v, want %v", accepted, tc.accepted)
					}
					if !accepted && !bytes.Equal(got, original) {
						t.Fatal("rejection changed the host login")
					}
				})
			}
		})
	}
}

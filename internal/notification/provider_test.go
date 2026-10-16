package notification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCLIProviderSignedBootstrapAndIsolatedEnvironment(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			BotID      string `json:"bot_id"`
			Time       int64  `json:"time"`
			Nonce      string `json:"nonce"`
			Signature  string `json:"signature"`
			BindSource int    `json:"bind_source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		sum := sha256.Sum256([]byte("secret" + request.BotID + strconv.FormatInt(request.Time, 10) + request.Nonce))
		if request.BotID != "bot" || request.Signature != hex.EncodeToString(sum[:]) || request.BindSource != 1 {
			t.Error("invalid signed bootstrap")
		}
		_, _ = w.Write([]byte(`{"errcode":0,"token":"test-token"}`))
	}))
	defer server.Close()
	script := filepath.Join(t.TempDir(), "cli")
	body := `#!/bin/sh
[ "$WECOM_CLI_ACCESS_TOKEN" = "test-token" ] || exit 1
[ -z "$WECOM_CLI_BASE_URL" ] || exit 1
[ -d "$WECOM_CLI_CONFIG_DIR" ] || exit 1
printf '{"sessions":[{"chat_id":"opaque","chat_name":"研发群","chat_type":"group","last_msg_time":"2026-10-09 10:00:00"}],"extra_identity_context":"untrusted ignored"}'
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECOM_CLI_BASE_URL", "https://untrusted.example")
	provider := NewCLIProvider()
	provider.authURL = server.URL
	provider.Binary = script
	chats, err := provider.Sessions(context.Background(), "bot", "secret")
	if err != nil || len(chats) != 1 || chats[0].Type != "group" || calls != 1 {
		t.Fatalf("sessions %v %v", chats, err)
	}
}
func TestProviderErrorDoesNotExposeServerResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":850003,"errmsg":"secret-value","token":"hidden-token"}`))
	}))
	defer server.Close()
	provider := NewCLIProvider()
	provider.authURL = server.URL
	_, err := provider.Sessions(context.Background(), "bot", "secret")
	if err == nil || strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "hidden-token") {
		t.Fatalf("unsafe error %v", err)
	}
}

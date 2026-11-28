package botinteraction

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestFeishuReplyRebuildsClientAfterInvalidTenantToken covers issue #1485.
// The SDK caches the tenant token per client. A long-lived feishuConn must
// drop that client when Feishu reports the cached token invalid, otherwise
// every retry reuses the stale token forever.
func TestFeishuReplyRebuildsClientAfterInvalidTenantToken(t *testing.T) {
	var mu sync.Mutex
	tokenCalls, replyCalls := 0, 0
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n := tokenCalls
		tokenCalls++
		mu.Unlock()
		token := "t-token"
		if n > 0 {
			token = "t-token-2" // a fresh token is issued after the old one is invalidated
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "tenant_access_token": token, "expire": 7200,
		})
	})
	mux.HandleFunc("/open-apis/im/v1/messages/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/reply") {
			t.Errorf("unexpected reply path %s", r.URL.Path)
		}
		mu.Lock()
		replyCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer t-token":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991663, "msg": "invalid tenant token"})
		case "Bearer t-token-2":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om_reply"}})
		default:
			t.Errorf("unexpected authorization %q", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 230001, "msg": "bad auth"})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ch := newFeishuChannel()
	ch.base = srv.URL
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli_app"}
	cred := Credential{Secret: "s"}
	conn := &feishuConn{channel: ch, bot: bot, cred: cred}
	conn.api = conn.freshAPI()
	msg := OutboundMessage{Text: "答复", SendKey: "token-retry-key", Route: ReplyRoute{MessageID: "om_1"}}

	first := conn.Reply(context.Background(), msg)
	if first.Code != "99991663" || !first.Retryable {
		t.Fatalf("first reply = %+v, want retryable 99991663", first)
	}
	second := conn.Reply(context.Background(), msg)
	if second.Outcome != Delivered || second.PlatformMessageID != "om_reply" {
		t.Fatalf("second reply = %+v, want delivered with the fresh token", second)
	}
	mu.Lock()
	tokens, replies := tokenCalls, replyCalls
	mu.Unlock()
	if tokens < 2 {
		t.Fatalf("token endpoint called %d time(s), want >= 2 after rebuilding the client", tokens)
	}
	if replies < 2 {
		t.Fatalf("reply endpoint called %d time(s), want >= 2", replies)
	}
}

// Rate limits and ordinary failures must not discard the client: rebuilding
// only clears the token cache and would not help either case.
func TestFeishuReplyKeepsClientOnRateLimitAndServerError(t *testing.T) {
	var mu sync.Mutex
	tokenCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		tokenCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "t-token", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/im/v1/messages/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991400, "msg": "rate limited"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ch := newFeishuChannel()
	ch.base = srv.URL
	bot := domain.NotificationBot{ID: "cfg", Channel: domain.BotChannelFeishu, BotID: "cli_app"}
	cred := Credential{Secret: "s"}
	conn := &feishuConn{channel: ch, bot: bot, cred: cred}
	conn.api = conn.freshAPI()
	msg := OutboundMessage{Text: "答复", Route: ReplyRoute{MessageID: "om_1"}}

	for i := 0; i < 2; i++ {
		got := conn.Reply(context.Background(), msg)
		if got.Code != "99991400" || !got.Retryable {
			t.Fatalf("reply %d = %+v, want retryable 99991400", i, got)
		}
	}
	mu.Lock()
	tokens := tokenCalls
	mu.Unlock()
	if tokens != 1 {
		t.Fatalf("token endpoint called %d time(s), want 1 (client must be kept)", tokens)
	}
}

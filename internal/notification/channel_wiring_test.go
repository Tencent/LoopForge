package notification

import (
	"agentflow/internal/botinteraction"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassifyProviderSend(t *testing.T) {
	cases := []struct {
		err  error
		want botinteraction.SendOutcome
		code string
	}{
		{nil, botinteraction.Delivered, ""},
		{failure(850003), botinteraction.Rejected, "850003"},
		{errNotificationContentLength, botinteraction.Rejected, "invalid_content"},
		{errors.New("企业微信未确认发送成功"), botinteraction.Unknown, "unconfirmed"},
	}
	for _, tc := range cases {
		if got := classifyProviderSend(tc.err); got.Outcome != tc.want || got.Code != tc.code {
			t.Errorf("%v: %+v", tc.err, got)
		}
	}
}

func TestCLIProviderVerifyUsesSignedBootstrapOnly(t *testing.T) {
	code := 0
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["bot_id"] != "bot" || body["signature"] == "" {
			t.Errorf("unsigned bootstrap: %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": code, "token": "tok"})
	}))
	defer server.Close()
	p := &CLIProvider{HTTP: server.Client(), authURL: server.URL}
	if err := p.Verify(context.Background(), "bot", "secret"); err != nil {
		t.Fatal(err)
	}
	code = 853004
	var pe *ProviderError
	if err := p.Verify(context.Background(), "bot", "secret"); !errors.As(err, &pe) || pe.Code != 853004 {
		t.Fatalf("invalid secret = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestNewWiresWeComChannelPushToCurrentProvider(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := New(store)
	provider := &fakeProvider{}
	svc.Provider = provider
	bot := domain.NotificationBot{ID: "cfg", BotID: "wecom-bot"}
	ch, err := svc.Interactions.Channel(bot)
	if err != nil {
		t.Fatal(err)
	}
	got := ch.(botinteraction.PushSender).Push(context.Background(), bot, botinteraction.Credential{Secret: "s"}, botinteraction.OutboundMessage{ChatID: "chat", Text: "hello"})
	if got.Outcome != botinteraction.Delivered || provider.sent != 1 || provider.content != "hello" {
		t.Fatalf("push = %+v sent=%v", got, provider.sent)
	}
}

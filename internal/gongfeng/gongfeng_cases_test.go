package gongfeng

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGongfengOAuthResponses(t *testing.T) {
	var tokenBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenBody = ""
			_ = r.ParseForm()
			tokenBody = r.Form.Encode()
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("code") == "bad-json" {
				_, _ = w.Write([]byte("{"))
				return
			}
			if r.Form.Get("code") == "missing" {
				_, _ = w.Write([]byte(`{"error":"denied"}`))
				return
			}
			if r.Form.Get("refresh_token") == "http-error" {
				http.Error(w, "secret", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"access","expires_in":60}`))
		case "/api/user":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"username":" alice "}`))
		case "/api/bad":
			http.Error(w, "no", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app/cb", AuthorizeURL: server.URL + "/authorize", TokenURL: server.URL + "/token", APIBase: server.URL + "/api"}
	u, err := AuthorizeURL(cfg, "state")
	if err != nil || !strings.Contains(u, "scope=") || !strings.Contains(u, "state=state") {
		t.Fatalf("authorize = %q, %v", u, err)
	}
	tok, err := Exchange(context.Background(), cfg, "ok")
	if err != nil || tok.AccessToken != "access" || !strings.Contains(tokenBody, "grant_type=authorization_code") {
		t.Fatalf("exchange = %#v, %v, %s", tok, err, tokenBody)
	}
	tok, err = Refresh(context.Background(), cfg, "refresh")
	if err != nil || tok.RefreshToken != "refresh" {
		t.Fatalf("refresh = %#v, %v", tok, err)
	}
	user, err := Username(context.Background(), cfg, "access")
	if err != nil || user != " alice " {
		t.Fatalf("username = %q, %v", user, err)
	}
	for _, code := range []string{"bad-json", "missing"} {
		if _, err := Exchange(context.Background(), cfg, code); err == nil {
			t.Fatalf("code %q accepted", code)
		}
	}
	if _, err := Refresh(context.Background(), cfg, "http-error"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("HTTP error/redaction = %v", err)
	}
	badCfg := cfg
	badCfg.APIBase = server.URL + "/api/bad"
	if _, err := Username(context.Background(), badCfg, "access"); err == nil {
		t.Fatal("bad user HTTP accepted")
	}
}

func TestGongfengConfigHelpers(t *testing.T) {
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", " id ")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", " secret ")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI", " cb ")
	cfg := ConfigFromEnv()
	if !cfg.Enabled() || cfg.ClientID != "id" {
		t.Fatalf("config = %#v", cfg)
	}
	if _, err := AuthorizeURL(Config{}, "x"); err == nil {
		t.Fatal("disabled config accepted")
	}
	badURL := cfg
	badURL.AuthorizeURL = "://%"
	if _, err := AuthorizeURL(badURL, "x"); err == nil {
		t.Fatal("malformed authorize URL accepted")
	}
	if _, err := requestToken(context.Background(), Config{}, nil); err == nil {
		t.Fatal("disabled token accepted")
	}
	now := time.Unix(100, 0)
	if (Token{}).Expiry(now).Sub(now) != defaultTTL || (Token{ExpiresIn: 3}).Expiry(now).Sub(now) != 3*time.Second {
		t.Fatal("expiry")
	}
	if got := redact(Config{ClientSecret: "secret"}, "x secret y"); got != "x *** y" {
		t.Fatalf("redact = %q", got)
	}
	if got := truncate("  12345 ", 3); got != "123" {
		t.Fatalf("truncate = %q", got)
	}
}

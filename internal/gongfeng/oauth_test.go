package gongfeng

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthorizeURLCarriesAppAndScope(t *testing.T) {
	cfg := Config{ClientID: "app", ClientSecret: "sec", RedirectURI: "http://127.0.0.1:18767/api/auth/gongfeng/callback", AuthorizeURL: "https://git.example.com/oauth/authorize"}
	got, err := AuthorizeURL(cfg, "state-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"client_id=app", "response_type=code", "state=state-1", "read_repository", "write_repository", "127.0.0.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("authorize URL missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "sec") {
		t.Fatal("client secret leaked into the authorize URL")
	}
}

func TestExchangeAndRefresh(t *testing.T) {
	var gotForm string
	mode := "exchange"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm = string(body)
		w.Header().Set("Content-Type", "application/json")
		if mode == "refresh" {
			_, _ = w.Write([]byte(`{"access_token":"access-2","expires_in":100}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":7200,"token_type":"bearer"}`))
	}))
	t.Cleanup(srv.Close)
	cfg := Config{ClientID: "app", ClientSecret: "sec", RedirectURI: "http://127.0.0.1/cb", TokenURL: srv.URL}
	tok, err := Exchange(context.Background(), cfg, "code-1")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access-1" || tok.RefreshToken != "refresh-1" || tok.ExpiresIn != 7200 {
		t.Fatalf("%+v", tok)
	}
	if !strings.Contains(gotForm, "grant_type=authorization_code") || !strings.Contains(gotForm, "code=code-1") {
		t.Fatalf("form = %s", gotForm)
	}
	if tok.Expiry(time.Unix(0, 0).UTC()).Sub(time.Unix(0, 0).UTC()) != 2*time.Hour {
		t.Fatal("expected two-hour expiry")
	}

	mode = "refresh"
	next, err := Refresh(context.Background(), cfg, "refresh-1")
	if err != nil {
		t.Fatal(err)
	}
	if next.AccessToken != "access-2" || next.RefreshToken != "refresh-1" {
		t.Fatalf("refresh dropped the previous refresh token: %+v", next)
	}
	if !strings.Contains(gotForm, "grant_type=refresh_token") {
		t.Fatalf("refresh form = %s", gotForm)
	}
}

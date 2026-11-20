package gongfeng

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Username and requestToken both route through do; a transport failure must
// surface as the shared connection error instead of a status/parse error.
func TestOAuthCallersPropagateConnectionError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	base := server.URL
	server.Close()
	cfg := Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app/cb", AuthorizeURL: base + "/authorize", TokenURL: base + "/token", APIBase: base + "/api"}
	if _, err := Username(context.Background(), cfg, "access"); err == nil || !strings.HasPrefix(err.Error(), "无法连接工蜂：") {
		t.Fatalf("username error = %v", err)
	}
	if _, err := Exchange(context.Background(), cfg, "code"); err == nil || !strings.HasPrefix(err.Error(), "无法连接工蜂：") {
		t.Fatalf("exchange error = %v", err)
	}
	if _, err := Refresh(context.Background(), cfg, "refresh"); err == nil || !strings.HasPrefix(err.Error(), "无法连接工蜂：") {
		t.Fatalf("refresh error = %v", err)
	}
}

func TestOAuthCallersRejectUnusablePayloads(t *testing.T) {
	big := strings.Repeat("y", maxResponseBytes+4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api-blank/user":
			_, _ = w.Write([]byte(`{"username":"  "}`))
		case "/api-badjson/user":
			_, _ = w.Write([]byte("{"))
		case "/api-huge/user":
			// A valid object followed by more than the cap: the truncated body
			// must not parse, proving Username consumes the bounded read.
			_, _ = w.Write([]byte(`{"username":"` + big + `"}`))
		case "/token":
			_ = r.ParseForm()
			switch r.Form.Get("code") {
			case "empty":
				_, _ = w.Write([]byte(`{}`))
			case "described":
				_, _ = w.Write([]byte(`{"error":"denied","error_description":"bad secret for id"}`))
			case "huge-error":
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(big))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := Config{ClientID: "id", ClientSecret: "secret", RedirectURI: "https://app/cb", AuthorizeURL: server.URL + "/authorize", TokenURL: server.URL + "/token"}

	for _, api := range []string{"/api-blank", "/api-badjson", "/api-huge"} {
		c := cfg
		c.APIBase = server.URL + api
		if _, err := Username(context.Background(), c, "access"); err == nil || err.Error() != "工蜂用户信息无法解析" {
			t.Fatalf("%s username error = %v", api, err)
		}
	}

	if _, err := Exchange(context.Background(), cfg, "empty"); err == nil || !strings.Contains(err.Error(), "响应里没有 access_token") {
		t.Fatalf("empty token error = %v", err)
	}
	_, err := Exchange(context.Background(), cfg, "described")
	if err == nil || !strings.Contains(err.Error(), "bad *** for id") || strings.Contains(err.Error(), "denied") {
		t.Fatalf("described token error = %v", err)
	}
	_, err = Exchange(context.Background(), cfg, "huge-error")
	if err == nil || !strings.HasPrefix(err.Error(), "工蜂令牌接口返回 HTTP 502：") || len(err.Error()) > 512 {
		t.Fatalf("huge error = %.80v", err)
	}
}

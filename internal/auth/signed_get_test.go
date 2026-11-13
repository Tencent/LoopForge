package auth

import (
	"context"
	"net/http"
	"testing"
)

func TestNewSignedGetSetsRIOAndAcceptHeaders(t *testing.T) {
	c := NewClient(Config{PaaSID: "paas-1", AppToken: "token-1", AuthHost: "https://sso.example.com"})
	req, err := c.newSignedGet(context.Background(), "https://sso.example.com/auth?code=x", "application/json")
	if err != nil {
		t.Fatalf("newSignedGet: %v", err)
	}
	if req.Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", req.Method)
	}
	if got := req.URL.String(); got != "https://sso.example.com/auth?code=x" {
		t.Fatalf("url = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	if got := req.Header.Get("x-rio-paasid"); got != "paas-1" {
		t.Fatalf("x-rio-paasid = %q", got)
	}
	ts, nonce := req.Header.Get("x-rio-timestamp"), req.Header.Get("x-rio-nonce")
	if ts == "" || len(nonce) != 4 {
		t.Fatalf("timestamp=%q nonce=%q", ts, nonce)
	}
	want := GenerateRIOHeaders("paas-1", "token-1", ts, nonce)["x-rio-signature"]
	if got := req.Header.Get("x-rio-signature"); got != want {
		t.Fatalf("x-rio-signature = %q, want %q", got, want)
	}
}

func TestNewSignedGetPropagatesContext(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")
	c := NewClient(Config{PaaSID: "p", AppToken: "t"})
	req, err := c.newSignedGet(ctx, "https://sso.example.com/photo/1/a.png", "image/png")
	if err != nil {
		t.Fatalf("newSignedGet: %v", err)
	}
	if req.Context().Value(ctxKey{}) != "v" {
		t.Fatal("request context not propagated")
	}
	if got := req.Header.Get("Accept"); got != "image/png" {
		t.Fatalf("Accept = %q", got)
	}
}

func TestNewSignedGetInvalidURL(t *testing.T) {
	c := NewClient(Config{PaaSID: "p", AppToken: "t"})
	if _, err := c.newSignedGet(context.Background(), "://bad url", "application/json"); err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

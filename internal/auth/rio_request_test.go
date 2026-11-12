package auth

import (
	"context"
	"net/http"
	"testing"
)

func TestNewRIORequestSetsSignedHeaders(t *testing.T) {
	c := NewClient(Config{PaaSID: "paas_x", AppToken: "token_x"})
	req, err := c.newRIORequest(context.Background(), "http://sso.example/api?code=1", "application/json")
	if err != nil {
		t.Fatalf("newRIORequest: %v", err)
	}
	if req.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", req.Method)
	}
	if got := req.URL.String(); got != "http://sso.example/api?code=1" {
		t.Errorf("url = %s", got)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}
	if got := req.Header.Get("x-rio-paasid"); got != "paas_x" {
		t.Errorf("x-rio-paasid = %q", got)
	}
	ts, nonce := req.Header.Get("x-rio-timestamp"), req.Header.Get("x-rio-nonce")
	if ts == "" || len(nonce) != 4 {
		t.Fatalf("unexpected timestamp/nonce: %q/%q", ts, nonce)
	}
	want := GenerateRIOHeaders("paas_x", "token_x", ts, nonce)["x-rio-signature"]
	if got := req.Header.Get("x-rio-signature"); got != want {
		t.Errorf("x-rio-signature = %q, want %q", got, want)
	}
}

func TestNewRIORequestRejectsInvalidURL(t *testing.T) {
	c := NewClient(Config{PaaSID: "p", AppToken: "t"})
	if _, err := c.newRIORequest(context.Background(), "http://bad host/\x7f", "*/*"); err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

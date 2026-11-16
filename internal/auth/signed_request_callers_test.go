package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// assertSignedRIORequest 校验请求带有正确的 RIO 网关签名头和期望的 Accept 头。
func assertSignedRIORequest(t *testing.T, r *http.Request, paasID, token, wantAccept string) {
	t.Helper()
	if r.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", r.Method)
	}
	if got := r.Header.Get("Accept"); got != wantAccept {
		t.Errorf("Accept = %q, want %q", got, wantAccept)
	}
	if got := r.Header.Get("x-rio-paasid"); got != paasID {
		t.Errorf("x-rio-paasid = %q, want %q", got, paasID)
	}
	ts, nonce := r.Header.Get("x-rio-timestamp"), r.Header.Get("x-rio-nonce")
	if ts == "" || nonce == "" {
		t.Fatalf("missing timestamp/nonce: %q/%q", ts, nonce)
	}
	want := GenerateRIOHeaders(paasID, token, ts, nonce)["x-rio-signature"]
	if got := r.Header.Get("x-rio-signature"); got != want {
		t.Errorf("x-rio-signature = %q, want %q", got, want)
	}
}

func TestExchangeCodeSendsSignedJSONRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertSignedRIORequest(t, r, "paas_ex", "token_ex", "application/json")
		_ = json.NewEncoder(w).Encode(IOAAuthResponse{Data: &IOAAuthData{StaffId: 7, LoginName: "alice"}})
	}))
	defer server.Close()

	c := NewClient(Config{PaaSID: "paas_ex", AppToken: "token_ex", AuthHost: server.URL, AuthURI: "/auth"})
	user, err := c.ExchangeCode(context.Background(), "code-1")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if user.IsGuest || user.LoginName != "alice" {
		t.Fatalf("user = %+v, want non-guest alice", user)
	}
}

func TestFetchPhotoSendsSignedImageRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertSignedRIORequest(t, r, "paas_ph", "token_ph", "image/png,image/*;q=0.8,*/*;q=0.5")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
	}))
	defer server.Close()

	c := NewClient(Config{PaaSID: "paas_ph", AppToken: "token_ph", AuthHost: server.URL, PhotoURI: "/photo"})
	if _, ct, err := c.FetchPhoto(context.Background(), "alice", 100); err != nil || ct != "image/png" {
		t.Fatalf("FetchPhoto = ct %q err %v, want image/png nil", ct, err)
	}
}

// 构造请求失败（非法 AuthHost）时：ExchangeCode 降级为访客，FetchPhoto 返回包装后的错误且不缓存。
func TestSignedRequestBuildFailureAtCallers(t *testing.T) {
	cfg := Config{PaaSID: "p", AppToken: "t", AuthHost: "http://bad host\x7f", AuthURI: "/auth", PhotoURI: "/photo"}
	c := NewClient(cfg)

	user, err := c.ExchangeCode(context.Background(), "code-1")
	if err != nil {
		t.Fatalf("ExchangeCode err = %v, want nil (guest fallback)", err)
	}
	if !user.IsGuest {
		t.Fatalf("user = %+v, want guest fallback", user)
	}

	_, _, err = c.FetchPhoto(context.Background(), "alice", 100)
	if err == nil || !strings.Contains(err.Error(), "create photo request failed") {
		t.Fatalf("FetchPhoto err = %v, want create photo request failed", err)
	}
	if errors.Is(err, ErrPhotoNotFound) || errors.Is(err, ErrCredentialsNotConfigured) {
		t.Fatalf("FetchPhoto err = %v, should not be a sentinel error", err)
	}
	if _, _, ok := c.photoCache.get("100:alice"); ok {
		t.Fatal("failed request must not populate photo cache")
	}
}

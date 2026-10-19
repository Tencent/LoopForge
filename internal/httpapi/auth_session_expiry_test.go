package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
)

// 回归用例：浏览器会话的 AuthTime 超过 authSessionTTL 后，/api/auth/current 必须
// 回落到访客身份并清掉过期 Cookie，让前端把用户重新送到 Passport。
// 修复前这里会用原始 AuthTime 重新签发 Cookie，形成"Cookie 一直续期、AuthTime
// 早已过期"的半失效状态：/api/auth/current 报告已登录，受保护接口却一律 401，
// 用户永久卡在「启动失败：authentication required」页面且无法自愈。
func TestAuthCurrent_ExpiredSessionFallsBackToGuest(t *testing.T) {
	authClient := auth.NewClient(auth.Config{
		AppDomain:     "dns-afp.example.com",
		SessionSecret: "secret-session-expiry",
	})
	handler, _ := newTestAuthServer(t, authClient)

	expired := &auth.User{
		StaffID:   1001,
		LoginName: "expired_user",
		IsGuest:   false,
		AuthTime:  time.Now().UTC().Add(-authSessionTTL - time.Hour),
	}
	token, err := authClient.SignUser(expired)
	if err != nil {
		t.Fatalf("sign user failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		User     auth.User `json:"user"`
		LoginURL string    `json:"loginUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if !body.User.IsGuest {
		t.Fatalf("expected expired session to be reported as guest, got %+v", body.User)
	}
	if body.LoginURL == "" {
		t.Errorf("expected loginUrl so the Studio can bounce the user back to Passport")
	}

	// 必须清掉过期 Cookie，否则浏览器会一直带着它重试，永远走不到访客分支。
	cleared := false
	for _, raw := range rec.Header().Values("Set-Cookie") {
		if !strings.Contains(raw, authCookieName+"=") {
			continue
		}
		if strings.Contains(raw, "Max-Age=0") || strings.Contains(raw, "MaxAge=0") {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("expected expired cookie to be cleared, got Set-Cookie: %v", rec.Header().Values("Set-Cookie"))
	}
}

// 有效会话必须照旧保持登录，并续签 Cookie（防止修复把正常会话也踢成访客）。
func TestAuthCurrent_FreshSessionStaysAuthenticated(t *testing.T) {
	authClient := auth.NewClient(auth.Config{
		AppDomain:     "dns-afp.example.com",
		SessionSecret: "secret-session-expiry",
	})
	handler, st := newTestAuthServer(t, authClient)

	if err := st.PutUser(domain.User{LoginName: "fresh_user", ChineseName: "有效会话员"}); err != nil {
		t.Fatalf("seed user failed: %v", err)
	}

	token, err := authClient.SignUser(&auth.User{
		LoginName: "fresh_user",
		IsGuest:   false,
		AuthTime:  time.Now().UTC().Add(-authSessionTTL + time.Hour),
	})
	if err != nil {
		t.Fatalf("sign user failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		User auth.User `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if body.User.IsGuest || body.User.LoginName != "fresh_user" {
		t.Fatalf("expected authenticated fresh_user, got %+v", body.User)
	}
}

// requestUser 是受保护接口的 401 判定来源，必须与 authCurrent 使用同一时长。
func TestRequestUser_ExpiredSessionRejected(t *testing.T) {
	authClient := auth.NewClient(auth.Config{SessionSecret: "secret-session-expiry"})
	handler, st := newTestAuthServer(t, authClient)
	_ = handler

	if err := st.PutUser(domain.User{LoginName: "stale_user", ChineseName: "过期会话员"}); err != nil {
		t.Fatalf("seed user failed: %v", err)
	}
	d := deps{repo: st, authClient: authClient}

	expiredToken, err := authClient.SignUser(&auth.User{
		LoginName: "stale_user",
		IsGuest:   false,
		AuthTime:  time.Now().UTC().Add(-authSessionTTL - time.Minute),
	})
	if err != nil {
		t.Fatalf("sign user failed: %v", err)
	}
	expiredReq := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	expiredReq.AddCookie(&http.Cookie{Name: authCookieName, Value: expiredToken})
	if user, ok := d.requestUser(expiredReq, true); ok {
		t.Fatalf("expected expired session to be rejected, got user %+v", user)
	}

	freshToken, err := authClient.SignUser(&auth.User{
		LoginName: "stale_user",
		IsGuest:   false,
		AuthTime:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("sign user failed: %v", err)
	}
	freshReq := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	freshReq.AddCookie(&http.Cookie{Name: authCookieName, Value: freshToken})
	if _, ok := d.requestUser(freshReq, true); !ok {
		t.Fatalf("expected fresh session to be accepted")
	}
}

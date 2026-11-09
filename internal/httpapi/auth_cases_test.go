package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
)

func TestAuthTokenLifecycleAndValidation(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, tc := range []struct {
		body any
		want int
	}{
		{map[string]any{"name": strings.Repeat("x", 129)}, http.StatusBadRequest},
		{map[string]any{"name": "bad-days", "expiresInDays": -1}, http.StatusBadRequest},
		{map[string]any{"name": "too-long", "expiresInDays": 366}, http.StatusBadRequest},
		{map[string]any{"name": "default-expiry"}, http.StatusCreated},
	} {
		rec := cvBRequest(t, f, http.MethodPost, "/api/auth/tokens", tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("token create = %d want %d: %s", rec.Code, tc.want, rec.Body.String())
		}
	}
	list := cvBRequest(t, f, http.MethodGet, "/api/auth/tokens", nil, true)
	if list.Code != http.StatusOK {
		t.Fatalf("token list = %d: %s", list.Code, list.Body.String())
	}
	var tokens []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &tokens); err != nil || len(tokens) == 0 {
		t.Fatalf("tokens: %v %s", err, list.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/auth/tokens/"+tokens[0].ID, nil, true); rec.Code != http.StatusOK {
		t.Fatalf("token revoke = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/auth/tokens/"+tokens[0].ID, nil, true); rec.Code != http.StatusNotFound {
		t.Fatalf("token second revoke = %d: %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/tokens", nil)
	req.Header.Set("Authorization", "Bearer invalid")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bearer token management = %d: %s", rec.Code, rec.Body.String())
	}
	if token, err := newAPIToken(); err != nil || !strings.HasPrefix(token, "afp_") {
		t.Fatalf("new token = %q, %v", token, err)
	}
	if tokenPrefix("short") != "short" || len(tokenPrefix(strings.Repeat("x", 20))) != 12 {
		t.Fatal("tokenPrefix")
	}
}

func TestAuthPathPreviewAndLocalLogin(t *testing.T) {
	for path, want := range map[string]bool{
		insightIngestPath: false, "/mcp": true, "/static/x": false,
		"/api/health": false, "/api/auth/photo/user/100": false,
		"/api/auth/tokens": false, "/api/auth/tokens/id": false,
		"/api/projects": true, "/api/runners/tasks/id/start": false,
	} {
		if got := protectedAccessPath(path); got != want {
			t.Errorf("protectedAccessPath(%q)=%v want %v", path, got, want)
		}
	}
	if _, ok := bearerToken(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Fatal("empty bearer accepted")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "bEaReR value")
	if raw, ok := bearerToken(req); !ok || raw != "value" {
		t.Fatal("case-insensitive bearer rejected")
	}

	f := cvBNewHTTPFixture(t)
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "1")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", f.user.LoginName)
	user, ok := (deps{repo: f.s}).previewLocalAuthUser()
	if !ok || user.LoginName != f.user.LoginName {
		t.Fatalf("preview user = %+v %v", user, ok)
	}
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "missing")
	if _, ok := (deps{repo: f.s}).previewLocalAuthUser(); ok {
		t.Fatal("missing preview user accepted")
	}

	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", f.user.LoginName)
	localReq := httptest.NewRequest(http.MethodPost, "http://example.test/api/auth/local", bytes.NewReader([]byte("{}")))
	localReq.RemoteAddr = "127.0.0.1:1234"
	localReq.Host = "example.test"
	localReq.Header.Set("Origin", "http://example.test")
	localReq.Header.Set("Content-Type", "application/json")
	localRec := httptest.NewRecorder()
	f.h.ServeHTTP(localRec, localReq)
	if localRec.Code != http.StatusOK {
		t.Fatalf("local login = %d: %s", localRec.Code, localRec.Body.String())
	}
}

func TestAuthHelperAndCurrentGatewayBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "true")
	req := httptest.NewRequest(http.MethodGet, "http://example.test/api/auth/current", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header["x-StAfF-NaMe"] = []string{f.user.LoginName}
	req.Header["x-StAfF-iD"] = []string{"123"}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), f.user.LoginName) {
		t.Fatalf("gateway current = %d: %s", rec.Code, rec.Body.String())
	}

	h := &authHandlers{deps: &deps{repo: f.s}}
	for _, rawURL := range []string{
		"http://host.test/?return_url=https://return.test/x",
		"http://host.test/",
	} {
		r := httptest.NewRequest(http.MethodGet, rawURL, nil)
		if rawURL == "http://host.test/" {
			r.Header.Set("Referer", "https://referer.test/")
		}
		if h.getReturnURL(r) == "" {
			t.Fatal("empty return URL")
		}
	}
	if !authSessionExpired(&auth.User{AuthTime: time.Now().Add(-8 * 24 * time.Hour)}) ||
		authSessionExpired(&auth.User{}) || authSessionExpired(nil) {
		t.Fatal("auth session expiry")
	}
}

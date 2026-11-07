package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/gongfeng"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestGongfengOAuthCallbackStoresGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "example-owner", ChineseName: "测试"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("example-owner")
	client := auth.NewClient(auth.Config{SessionSecret: "oauth-test-secret"})
	h := authHandlers{deps: &deps{repo: st, authClient: client}}

	gongfengUser := "example-owner"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":7200}`))
		case "/api/v3/user":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"username":"` + gongfengUser + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", "app-id")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", "app-secret")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI", "http://127.0.0.1:18767/api/auth/gongfeng/callback")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_TOKEN_URL", srv.URL+"/token")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_API_BASE", srv.URL+"/api/v3")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_AUTHORIZE_URL", srv.URL+"/authorize")

	state, err := signGongfengOAuthState(gongfengOAuthState{UserID: user.ID, Exp: time.Now().Add(time.Minute).Unix(), Nonce: "n"}, "oauth-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/gongfeng/callback?code=once&state="+url.QueryEscape(state), nil)
	h.gongfengOAuthCallback(c)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "http://127.0.0.1:18767/gongfeng-oauth-done.html" {
		t.Fatalf("location=%s", got)
	}
	saved, _ := st.User(user.ID)
	if saved.GitToken != "access-1" || saved.GongfengRefreshToken != "refresh-1" {
		t.Fatalf("stored=%+v", saved)
	}

	// Links started before the done page carry return=quick-access; they
	// must land on the same page instead of opening a second workbench.
	quick, err := signGongfengOAuthState(gongfengOAuthState{UserID: user.ID, Exp: time.Now().Add(time.Minute).Unix(), Nonce: "n1", Return: "quick-access"}, "oauth-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/gongfeng/callback?code=quick&state="+url.QueryEscape(quick), nil)
	h.gongfengOAuthCallback(c)
	if got := rec.Header().Get("Location"); rec.Code != http.StatusFound || got != "http://127.0.0.1:18767/gongfeng-oauth-done.html" {
		t.Fatalf("quick-access status=%d location=%s", rec.Code, got)
	}

	other, err := signGongfengOAuthState(gongfengOAuthState{UserID: user.ID, Exp: time.Now().Add(time.Minute).Unix(), Nonce: "n2"}, "oauth-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	gongfengUser = "someone-else"
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/gongfeng/callback?code=twice&state="+url.QueryEscape(other), nil)
	h.gongfengOAuthCallback(c)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Location") != "" {
		t.Fatalf("mismatch status=%d body=%s", rec.Code, rec.Body.String())
	}
	saved, _ = st.User(user.ID)
	if saved.GitToken != "access-1" {
		t.Fatalf("mismatched account overwrote the token: %s", saved.GitToken)
	}
}

func TestGongfengOAuthStartRedirects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "example-owner"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("example-owner")
	client := auth.NewClient(auth.Config{SessionSecret: "oauth-test-secret"})
	h := authHandlers{deps: &deps{repo: st, authClient: client}}
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", "app-id")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", "app-secret")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI", "http://127.0.0.1:18767/api/auth/gongfeng/callback")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_AUTHORIZE_URL", "https://git.example.com/oauth/authorize")

	signed, err := client.SignUser(&auth.User{ID: user.ID, LoginName: user.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/gongfeng/start", nil)
	c.Request.AddCookie(&http.Cookie{Name: authCookieName, Value: signed})
	h.gongfengOAuthStart(c)
	if rec.Code != http.StatusFound {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.Contains(location, "client_id=app-id") || !strings.Contains(location, "read_repository") || strings.Contains(location, "app-secret") {
		t.Fatalf("location=%s", location)
	}
}

// With sign-in enforced, a signed-out browser that follows "用工蜂授权" must
// land on a page telling it to sign in, and the settings page must be able to
// tell whether the server has a Gongfeng application at all.
func TestGongfengOAuthEntryPointsThroughAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "example-owner"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("example-owner")
	client := auth.NewClient(auth.Config{SessionSecret: "oauth-test-secret"})
	d := &deps{repo: st, authClient: client}
	h := authHandlers{deps: d}
	router := gin.New()
	router.Use((&tokenHandlers{deps: d}).userAccessAuth())
	router.GET("/api/auth/gongfeng", h.gongfengOAuthStatus)
	router.GET("/api/auth/gongfeng/start", h.gongfengOAuthStart)
	signed, err := client.SignUser(&auth.User{ID: user.ID, LoginName: user.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, signedIn bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if signedIn {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: signed})
		}
		router.ServeHTTP(rec, req)
		return rec
	}
	for _, key := range []string{"AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", "AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", "AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI"} {
		t.Setenv(key, "")
	}
	if rec := get("/api/auth/gongfeng", true); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"enabled":false}` {
		t.Fatalf("unconfigured status = %d %s", rec.Code, rec.Body.String())
	}
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", "app-id")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", "app-secret")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI", "http://127.0.0.1:18767/api/auth/gongfeng/callback")
	if rec := get("/api/auth/gongfeng", true); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"enabled":true}` {
		t.Fatalf("configured status = %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("/api/auth/gongfeng", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status probe must stay behind sign-in, got %d", rec.Code)
	}
	rec := get("/api/auth/gongfeng/start", false)
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "请先登录") {
		t.Fatalf("signed-out start = %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if rec := get("/api/auth/gongfeng/start", true); rec.Code != http.StatusFound {
		t.Fatalf("signed-in start = %d %s", rec.Code, rec.Body.String())
	}
}

func TestGongfengOAuthReturnURLKeepsRedirectOrigin(t *testing.T) {
	for redirect, want := range map[string]string{
		"https://agentflow.example.com/api/auth/gongfeng/callback?x=1": "https://agentflow.example.com/gongfeng-oauth-done.html",
		"http://127.0.0.1:8080/api/auth/gongfeng/callback":             "http://127.0.0.1:8080/gongfeng-oauth-done.html",
		"/api/auth/gongfeng/callback":                                  "/gongfeng-oauth-done.html",
		"::bad":                                                        "/gongfeng-oauth-done.html",
	} {
		if got := gongfengOAuthReturnURL(gongfeng.Config{RedirectURI: redirect}); got != want {
			t.Errorf("redirect %q: got %q want %q", redirect, got, want)
		}
	}
}

// A state must never verify under a key anyone can read in the source.
func TestGongfengOAuthStateNeedsSessionSecret(t *testing.T) {
	state := gongfengOAuthState{UserID: 1, Exp: time.Now().Add(time.Minute).Unix(), Nonce: "n"}
	if _, err := signGongfengOAuthState(state, " "); err == nil {
		t.Fatal("signed a state without a secret")
	}
	forged, err := signGongfengOAuthState(state, "agentflow-gongfeng-oauth")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseGongfengOAuthState(forged, ""); err == nil {
		t.Fatal("accepted a state signed with the old built-in key")
	}
	if _, err := parseGongfengOAuthState(forged, "real-secret"); err == nil {
		t.Fatal("accepted a state signed with another key")
	}
	if got, err := parseGongfengOAuthState(forged, "agentflow-gongfeng-oauth"); err != nil || got.UserID != 1 {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

package httpapi

import (
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/auth"
	"agentflow/internal/engine"

	"github.com/gin-gonic/gin"
)

func TestAuthTokenPureBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "not-a-bool")
	if authRequired() {
		t.Fatal("invalid auth-required value enabled auth")
	}
	for path, want := range map[string]bool{
		insightIngestPath: false, "/mcp": true, "/other": false,
		"/api/health": false, "/api/auth/photo/name": false, "/api/auth/tokens": false,
		"/api/projects": true, "/runners/heartbeat": false,
	} {
		if got := protectedAccessPath(path); got != want {
			t.Errorf("protectedAccessPath(%q)=%v want %v", path, got, want)
		}
	}
	for value, want := range map[string]bool{
		"": false, "Basic x": false, "Bearer": false, "Bearer ": false, "bearer token": true,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", value)
		_, got := bearerToken(req)
		if got != want {
			t.Errorf("bearerToken(%q)=%v", value, got)
		}
	}
	if tokenPrefix("short") != "short" || len(tokenPrefix(strings.Repeat("x", 20))) != 12 {
		t.Fatal("token prefix")
	}

	d := deps{repo: f.s}
	if d.annotateUser(nil) != nil {
		t.Fatal("nil annotation")
	}
	guest := &auth.User{IsGuest: true}
	if d.annotateUser(guest) != guest {
		t.Fatal("guest annotation")
	}
	plain := deps{}
	user := &auth.User{LoginName: "nobody"}
	if plain.annotateUser(user) != user || user.IsPlatformAdmin {
		t.Fatal("nil-store annotation")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(f.user)))
	if resolved, ok := d.requestUser(req, true); !ok || resolved.ID != f.user.ID {
		t.Fatalf("context user: %+v %v", resolved, ok)
	}

	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "1")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", "")
	if _, ok := d.previewLocalAuthUser(); ok {
		t.Fatal("empty preview login accepted")
	}
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", f.user.LoginName)
	if resolved, ok := d.previewLocalAuthUser(); !ok || resolved.ID != f.user.ID {
		t.Fatalf("preview user: %+v %v", resolved, ok)
	}

	middleware := (&tokenHandlers{deps: &d}).userAccessAuth()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodOptions, "/api/projects", nil)
	middleware(c)
}

func TestRunnerBinaryHelperBranches(t *testing.T) {
	root := t.TempDir()
	if got := sanitizePlatformParam(" Linux "); got != "linux" {
		t.Fatalf("sanitized = %q", got)
	}
	for _, value := range []string{"bad-value", strings.Repeat("a", 17)} {
		if sanitizePlatformParam(value) != "" {
			t.Errorf("unsafe platform accepted: %q", value)
		}
	}
	t.Setenv("AGENTFLOW_SOURCE_ROOT", root)
	if runnerSourceRoot() != "" {
		t.Fatal("invalid source root accepted")
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "agentflow-runner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runnerSourceRoot() != root {
		t.Fatal("valid source root not selected")
	}
	t.Setenv("AGENTFLOW_SOURCE_ROOT", "")
	if !dirExists(filepath.Join(root, "cmd")) || dirExists(filepath.Join(root, "missing")) {
		t.Fatal("directory helper")
	}

	digestRoot := t.TempDir()
	if _, err := runnerSourceDigest(digestRoot); err == nil {
		t.Fatal("incomplete source digest succeeded")
	}
	for _, dir := range []string{"cmd/agentflow-runner", "cmd/agentflow-gate-mcp", "internal/pkg/testdata"} {
		if err := os.MkdirAll(filepath.Join(digestRoot, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"go.mod": "module test\n", "go.sum": "", "cmd/agentflow-runner/main.go": "package main\n",
		"cmd/agentflow-runner/main_test.go": "ignored", "internal/pkg/x.go": "package pkg\n",
		"internal/pkg/testdata/ignored.go": "ignored",
	} {
		if err := os.WriteFile(filepath.Join(digestRoot, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if digest, err := runnerSourceDigest(digestRoot); err != nil || len(digest) != 64 {
		t.Fatalf("digest = %q %v", digest, err)
	}
	if got := clipBuildOutput([]byte(strings.Repeat("x", 500))); len(got) != 400 {
		t.Fatalf("clipped length = %d", len(got))
	}
}

func TestGinMiddlewareAndSmallViews(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	respond(c, engine.ErrExecutionDisabled)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("execution disabled status = %d", rec.Code)
	}

	router := gin.New()
	router.Use(securityHeaders(), gzipMaybe())
	router.GET("/asset.js", func(c *gin.Context) { c.String(http.StatusOK, "javascript") })
	router.GET("/api/example/stream", func(c *gin.Context) { c.String(http.StatusOK, "event") })
	req := httptest.NewRequest(http.MethodGet, "/asset.js?v=1", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset headers: %#v", rec.Header())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/example/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("stream was compressed")
	}
	rawRecorder := httptest.NewRecorder()
	testContext, _ := gin.CreateTestContext(rawRecorder)
	compressor := gzip.NewWriter(rawRecorder)
	writer := &gzipWriter{ResponseWriter: testContext.Writer, gz: compressor}
	if _, err := writer.WriteString("compressed"); err != nil {
		t.Fatal(err)
	}
	writer.Flush()
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}

	f := cvBNewHTTPFixture(t)
	verify := &usersHandlers{deps: &deps{repo: f.s}}
	invalidID := httptest.NewRecorder()
	invalidContext, _ := gin.CreateTestContext(invalidID)
	invalidContext.Request = httptest.NewRequest(http.MethodPost, "/api/users/nope/git-token/verify", strings.NewReader("{}"))
	invalidContext.Params = gin.Params{{Key: "id", Value: "nope"}}
	verify.verifyGitToken(invalidContext)
	if invalidID.Code != http.StatusBadRequest {
		t.Fatalf("invalid token user id = %d", invalidID.Code)
	}
	malformedToken := httptest.NewRecorder()
	malformedContext, _ := gin.CreateTestContext(malformedToken)
	malformedRequest := httptest.NewRequest(http.MethodPost, "/api/users/1/git-token/verify", strings.NewReader("{"))
	malformedContext.Request = malformedRequest.WithContext(context.WithValue(malformedRequest.Context(), requestUserContextKey{}, domainUserToAuth(f.user)))
	malformedContext.Params = gin.Params{{Key: "id", Value: "1"}}
	verify.verifyGitToken(malformedContext)
	if malformedToken.Code != http.StatusBadRequest {
		t.Fatalf("malformed token verify = %d", malformedToken.Code)
	}
	views, err := (issuesHandlers{deps: &deps{repo: f.s, engine: f.e}}).issueDeletionViews(nil)
	if err != nil || views != nil {
		t.Fatalf("nil issue views: %#v %v", views, err)
	}
}

func TestGongfengOAuthAuthenticatedStart(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_ID", "client")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_REDIRECT_URI", "https://example.test/callback")
	call := func(client *auth.Client, want int) {
		h := &authHandlers{deps: &deps{repo: f.s, authClient: client}}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest(http.MethodGet, "/api/auth/gongfeng/start?return=quick-access", nil)
		c.Request = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(f.user)))
		h.gongfengOAuthStart(c)
		if rec.Code != want {
			t.Fatalf("OAuth start = %d want %d: %s", rec.Code, want, rec.Body.String())
		}
	}
	call(auth.NewClient(auth.Config{SessionSecret: "oauth-secret"}), http.StatusFound)
}

func TestAuthCurrentGatewayAndPreviewBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	stored := f.user
	stored.ChineseName = "Gateway User"
	stored.DeptID = 42
	stored.DeptName = "Engineering"
	stored.StaffID = 99
	if err := f.s.PutUser(stored); err != nil {
		t.Fatal(err)
	}
	h := &authHandlers{deps: &deps{repo: f.s, authClient: auth.NewClient(auth.Config{SessionSecret: "secret"})}}
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "1")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Staffname", stored.LoginName)
	c.Request = req
	h.authCurrent(c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Engineering") {
		t.Fatalf("gateway current = %d: %s", rec.Code, rec.Body.String())
	}

	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "1")
	t.Setenv("AGENTFLOW_LOCAL_LOGIN_USER", stored.LoginName)
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/current", nil)
	h.authCurrent(c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), stored.LoginName) {
		t.Fatalf("preview current = %d: %s", rec.Code, rec.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/auth?return_url=https://example.com/path", nil)
	if got := h.getReturnURL(request); got != "https://example.com/path" {
		t.Fatalf("return URL = %q", got)
	}
	request = httptest.NewRequest(http.MethodGet, "/auth", nil)
	request.Header.Set("Referer", "https://example.com/ref")
	if got := h.getReturnURL(request); got != "https://example.com/ref" {
		t.Fatalf("referer URL = %q", got)
	}
	request = httptest.NewRequest(http.MethodGet, "/auth?return_url=https://return.test/path", nil)
	request.Header.Set("Referer", "https://referer.test/")
	if got := h.getReturnURL(request); got != "http://example.com/" {
		t.Fatalf("foreign return URL must fall back to site root, got %q", got)
	}
}

func TestUsersNilAndMalformedBranches(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-admin")
	h := &usersHandlers{deps: &deps{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/users", nil)
	h.users(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil users = %d", rec.Code)
	}

	admin := &auth.User{ID: 1, LoginName: "example-admin"}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/admins", nil)
	c.Request = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, admin))
	h.platformAdmins(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil platform admins = %d: %s", rec.Code, rec.Body.String())
	}

	f := cvBNewHTTPFixture(t)
	uh := &usersHandlers{deps: &deps{repo: f.s}}
	rec = cvBDirectHandlerRequest(t, f, http.MethodPut, "/users/x", nil, uh.user,
		map[string]string{"id": "invalid"}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid user id = %d", rec.Code)
	}
}

func TestTokenZeroIDAndEmptyDelete(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &tokenHandlers{deps: &deps{repo: f.s}}
	zero := &auth.User{LoginName: "cvb-zero-token-user", ChineseName: "Zero"}
	req := httptest.NewRequest(http.MethodGet, "/tokens", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, zero))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	h.authTokens(c)
	if rec.Code != http.StatusOK || zero.ID <= 0 {
		t.Fatalf("zero-id token owner = %d user=%+v body=%s", rec.Code, zero, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	req = httptest.NewRequest(http.MethodDelete, "/tokens/", nil)
	c.Request = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(f.user)))
	h.authToken(c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty token id = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "invalid"})
	if _, ok := h.requestUser(req, false); ok {
		t.Fatal("invalid signed cookie authenticated")
	}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	req = httptest.NewRequest(http.MethodPost, "/tokens", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(f.user)))
	h.authTokens(c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed token body = %d", rec.Code)
	}
}

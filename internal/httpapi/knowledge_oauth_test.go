package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/knowledge"

	"github.com/gin-gonic/gin"
)

// TestKnowledgeOAuthRoutesCoexist proves the static /oauth/callback route and
// the parameterised /oauth/:type/callback route can live in the same Gin tree
// (Gin panics at registration time on a real conflict, which New() would hit).
func TestKnowledgeOAuthRoutesCoexist(t *testing.T) {
	_, handler, _, _ := newKnowledgeTestServer(t)
	engine, ok := handler.(*gin.Engine)
	if !ok {
		t.Fatalf("handler is %T, want *gin.Engine", handler)
	}
	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /api/knowledge/oauth/presets",
		"POST /api/knowledge/oauth/:type/authorize",
		"GET /api/knowledge/oauth/callback",
		"GET /api/knowledge/oauth/:type/callback",
	} {
		if !registered[want] {
			t.Fatalf("route %q is not registered; routes=%v", want, engine.Routes())
		}
	}
	// authorize is a state-changing operation (pending row + DCR): POST only,
	// never reachable via cross-site GET.
	if registered["GET /api/knowledge/oauth/:type/authorize"] {
		t.Fatal("GET /api/knowledge/oauth/:type/authorize must not be registered")
	}

	// Both callback shapes must reach the handler (303 to the completion page),
	// not fall through to NoRoute/404.
	for _, path := range []string{
		"/api/knowledge/oauth/callback?state=bad&code=x",
		"/api/knowledge/oauth/feishu/callback?state=bad&code=x",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("GET %s = %d, want 303", path, rec.Code)
		}
		location := rec.Header().Get("Location")
		if !strings.HasPrefix(location, "/knowledge-oauth-done.html?status=error") {
			t.Fatalf("GET %s redirects to %q", path, location)
		}
	}
}

// TestKnowledgeOAuthCallbackIsPublic keeps the callback reachable without a
// session while the authorize endpoints stay protected.
func TestKnowledgeOAuthCallbackIsPublic(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/knowledge/oauth/callback":         false,
		"/api/knowledge/oauth/feishu/callback":  false,
		"/api/knowledge/oauth/feishu/authorize": true,
		"/api/knowledge/oauth/presets":          true,
		"/api/knowledge/oauth/a/b/callback":     true,
		"/api/knowledge/oauth/callback/extra":   true,
		"/api/knowledge/oauth/":                 true,
	} {
		if got := protectedAccessPath(path); got != want {
			t.Errorf("protectedAccessPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestKnowledgeOAuthPresetsEndpoint(t *testing.T) {
	_, handler, _, _ := newKnowledgeTestServer(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/knowledge/oauth/presets", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("presets = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"feishu"`, `"notion"`, `"redirectUri"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("presets body misses %q: %s", want, body)
		}
	}
	if strings.Contains(body, "clientSecret") || strings.Contains(body, "accessToken") {
		t.Fatalf("presets leaked credentials: %s", body)
	}
}

// TestKnowledgeOAuthDonePageStaysUnderGlobalCSP proves the completion page is
// served from the embedded web bundle, has no inline script and never reads
// the URL (the global policy is script-src 'self').
func TestKnowledgeOAuthDonePageStaysUnderGlobalCSP(t *testing.T) {
	_, handler, _, _ := newKnowledgeTestServer(t)
	get := func(path string) (*httptest.ResponseRecorder, string) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec, rec.Body.String()
	}

	rec, page := get("/knowledge-oauth-done.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET done page = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("done page CSP = %q", rec.Header().Get("Content-Security-Policy"))
	}
	if strings.Contains(page, "<script>") || !strings.Contains(page, `<script src="/knowledge-oauth-done.js" defer></script>`) {
		t.Fatalf("done page must load only its same-origin script")
	}
	for _, leak := range []string{"location.search", "location.hash"} {
		if strings.Contains(page, leak) {
			t.Fatalf("done page must not touch %q", leak)
		}
	}

	rec, script := get("/knowledge-oauth-done.js")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("GET done script = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		"new BroadcastChannel('agentflow-knowledge-oauth')",
		"postMessage({type:'knowledge-oauth-completed'})",
		"window.close()",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("done script misses %q", want)
		}
	}
	for _, leak := range []string{"location.search", "location.hash", "code", "state", "token"} {
		if strings.Contains(script, leak) {
			t.Fatalf("done script must not touch %q", leak)
		}
	}

	// Explicit embed listing check (serving above already proves it loads).
	embedSource, err := os.ReadFile("../../web/embed.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"knowledge-oauth-done.html", "knowledge-oauth-done.js"} {
		if !strings.Contains(string(embedSource), want) {
			t.Fatalf("web/embed.go misses %q", want)
		}
	}
}

type callbackEchoFake struct {
	mu       sync.Mutex
	verifier string
	code     string
}

func (f *callbackEchoFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/token" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = r.ParseForm()
	f.mu.Lock()
	f.verifier = r.PostForm.Get("code_verifier")
	f.code = r.PostForm.Get("code")
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"rejected cs-echo-secret code ` +
		r.PostForm.Get("code") + ` verifier ` + r.PostForm.Get("code_verifier") + `"}`))
}

// TestKnowledgeOAuthCallbackLocationIsCodeOnly proves the completion redirect
// carries only a fixed code, never upstream text or OAuth material.
func TestKnowledgeOAuthCallbackLocationIsCodeOnly(t *testing.T) {
	t.Setenv("LOOPFORGE_KNOWLEDGE_OAUTH_INSECURE", "1")
	_, handler, _, _ := newKnowledgeTestServer(t)

	fake := &callbackEchoFake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	createBody := fmt.Sprintf(`{"name":"cb","type":"mcp","endpoint":"http://127.0.0.1:1/mcp","config":{"extra":{"oauthMode":"custom","oauthTokenUrl":%q,"oauthAuthorizeUrl":%q,"oauthClientId":"cid"}},"secret":{"oauthClientSecret":"cs-echo-secret"}}`, srv.URL+"/token", srv.URL+"/authorize")
	rec := doJSON(t, handler, http.MethodPost, "/api/knowledge/connectors", createBody, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create connector = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode created connector: %v %s", err, rec.Body.String())
	}

	rec = doJSON(t, handler, http.MethodPost, "/api/knowledge/oauth/mcp/authorize", `{"connectorId":"`+created.ID+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize = %d: %s", rec.Code, rec.Body.String())
	}
	var authResp struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &authResp); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authResp.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("authorize url misses state")
	}
	nonce, err := knowledge.VerifyOAuthStateNonce(state)
	if err != nil {
		t.Fatal(err)
	}
	cookieName := knowledgeOAuthBrowserCookieName(nonce)
	var nonceCookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == cookieName {
			nonceCookie = ck
		}
	}
	if nonceCookie == nil || nonceCookie.Value == "" {
		t.Fatal("authorize response misses browser binding cookie")
	}

	// A second authorization in the same browser must not overwrite the first.
	rec = doJSON(t, handler, http.MethodPost, "/api/knowledge/oauth/mcp/authorize", `{"connectorId":"`+created.ID+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second authorize = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &authResp); err != nil {
		t.Fatal(err)
	}
	parsed, err = url.Parse(authResp.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	state2 := parsed.Query().Get("state")
	nonce2, err := knowledge.VerifyOAuthStateNonce(state2)
	if err != nil {
		t.Fatal(err)
	}
	cookieName2 := knowledgeOAuthBrowserCookieName(nonce2)
	var nonceCookie2 *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == cookieName2 {
			nonceCookie2 = ck
		}
	}
	if nonceCookie2 == nil || nonceCookie2.Value == "" || cookieName2 == cookieName {
		t.Fatal("parallel authorizations did not get independent browser cookies")
	}

	callbackPath := "/api/knowledge/oauth/mcp/callback?state=" + url.QueryEscape(state) + "&code=code-echo-123"
	// Without the browser binding cookie the callback must be rejected
	// (Login-CSRF), even with a valid signed state.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, callbackPath, nil))
	if loc := rec.Header().Get("Location"); loc != "/knowledge-oauth-done.html?status=error&code=state_invalid" {
		t.Fatalf("cookie-less callback location = %q", loc)
	}

	rec = httptest.NewRecorder()
	callbackReq := httptest.NewRequest(http.MethodGet, callbackPath, nil)
	callbackReq.AddCookie(nonceCookie)
	handler.ServeHTTP(rec, callbackReq)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d: %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if location != "/knowledge-oauth-done.html?status=error&code=oauth_failed" {
		t.Fatalf("callback location = %q", location)
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == cookieName2 {
			t.Fatal("first callback cleared the second authorization cookie")
		}
	}

	callbackPath2 := "/api/knowledge/oauth/mcp/callback?state=" + url.QueryEscape(state2) + "&code=code-echo-456"
	rec = httptest.NewRecorder()
	callbackReq = httptest.NewRequest(http.MethodGet, callbackPath2, nil)
	callbackReq.AddCookie(nonceCookie2)
	handler.ServeHTTP(rec, callbackReq)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/knowledge-oauth-done.html?status=error&code=oauth_failed" {
		t.Fatalf("second callback = %d: %s", rec.Code, rec.Body.String())
	}

	fake.mu.Lock()
	verifier := fake.verifier
	fake.mu.Unlock()
	if verifier == "" {
		t.Fatal("token endpoint never saw code_verifier")
	}
	for _, leak := range []string{"reason=", "cs-echo-secret", "code-echo-123", verifier,
		url.QueryEscape("cs-echo-secret"), url.QueryEscape(verifier)} {
		if strings.Contains(location, leak) {
			t.Fatalf("callback location leaked %q: %s", leak, location)
		}
	}
}

func TestKnowledgeOAuthErrorCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"connector changed", &knowledge.OAuthCallbackError{Code: knowledge.OAuthCallbackCodeConnectorChanged, Err: errors.New("x")}, "connector_changed"},
		{"state invalid", &knowledge.OAuthCallbackError{Code: knowledge.OAuthCallbackCodeStateInvalid, Err: errors.New("x")}, "state_invalid"},
		{"failed", &knowledge.OAuthCallbackError{Code: knowledge.OAuthCallbackCodeFailed, Err: errors.New("x")}, "oauth_failed"},
		{"unclassified", errors.New("boom"), "oauth_failed"},
	}
	for _, tc := range cases {
		if got := knowledgeOAuthErrorCode(tc.err); got != tc.want {
			t.Errorf("%s: code = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestKnowledgeOAuthInvalidStateDoesNotEmitEmptyCookie(t *testing.T) {
	_, handler, _, _ := newKnowledgeTestServer(t)
	for _, target := range []string{
		"/api/knowledge/oauth/mcp/callback",
		"/api/knowledge/oauth/mcp/callback?state=tampered.sig&code=abc",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: status = %d, want 303", target, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/knowledge-oauth-done.html?status=error&code=state_invalid" {
			t.Fatalf("%s: location = %q", target, loc)
		}
		if cookies := rec.Result().Cookies(); len(cookies) != 0 {
			t.Fatalf("%s: unexpected Set-Cookie headers %+v", target, cookies)
		}
	}
}

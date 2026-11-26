package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

// fakeOAuthProvider emulates an MCP gateway that requires OAuth 2.1 and the
// authorization server in front of it (RFC 9728 + RFC 8414 + RFC 7591).
type fakeOAuthProvider struct {
	mu               sync.Mutex
	baseURL          string
	requests         []fakeMCPRequest
	resourceMetaHits int
	registerCalls    int
	tokenCalls       int

	// expectations filled by the test before the callback.
	wantChallenge string
	wantResource  string
	wantRedirect  string
}

type fakeMCPRequest struct {
	Method  string
	Query   string
	Auth    string
	Session string
}

func (f *fakeOAuthProvider) setBaseURL(u string) {
	f.mu.Lock()
	f.baseURL = u
	f.mu.Unlock()
}

func (f *fakeOAuthProvider) base() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.baseURL
}

func (f *fakeOAuthProvider) setExpectations(challenge, resource, redirect string) {
	f.mu.Lock()
	f.wantChallenge, f.wantResource, f.wantRedirect = challenge, resource, redirect
	f.mu.Unlock()
}

func (f *fakeOAuthProvider) expectations() (challenge, resource, redirect string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wantChallenge, f.wantResource, f.wantRedirect
}

func (f *fakeOAuthProvider) record(r *http.Request, method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, fakeMCPRequest{
		Method:  method,
		Query:   r.URL.RawQuery,
		Auth:    r.Header.Get("Authorization"),
		Session: r.Header.Get("Mcp-Session-Id"),
	})
}

func (f *fakeOAuthProvider) mcpRequests() []fakeMCPRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMCPRequest(nil), f.requests...)
}

func writeJSONBody(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeOAuthProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/mcp":
		f.serveMCP(w, r)
	case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
		f.mu.Lock()
		f.resourceMetaHits++
		f.mu.Unlock()
		writeJSONBody(w, map[string]any{
			"resource":              f.base() + "/mcp?company_from=xxx",
			"authorization_servers": []string{f.base()},
			"scopes_supported":      []string{"kb.read"},
		})
	case "/.well-known/oauth-authorization-server":
		writeJSONBody(w, map[string]any{
			"issuer":                                f.base(),
			"authorization_endpoint":                f.base() + "/authorize",
			"token_endpoint":                        f.base() + "/token",
			"registration_endpoint":                 f.base() + "/register",
			"scopes_supported":                      []string{"kb.read"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
			"resource_indicators_supported":         true,
		})
	case "/register":
		f.serveRegister(w, r)
	case "/token":
		f.serveToken(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeOAuthProvider) serveMCP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)
	f.record(r, req.Method)

	if r.Header.Get("Authorization") != "Bearer at-1" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="mcp", resource_metadata="`+f.base()+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-abc")
		writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		if r.Header.Get("Mcp-Session-Id") != "sess-abc" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"missing session"}`))
			return
		}
		writeJSONBody(w, map[string]any{
			"jsonrpc": "2.0", "id": 2,
			"result": map[string]any{"tools": []map[string]any{{
				"name": "kb_search", "description": "search",
				"inputSchema": map[string]any{"type": "object"},
				"annotations": map[string]any{"readOnlyHint": true},
			}}},
		})
	case "tools/call":
		if r.Header.Get("Mcp-Session-Id") != "sess-abc" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"missing session"}`))
			return
		}
		text, _ := json.Marshal(map[string]any{"results": []map[string]any{{
			"source": "src-1", "source_name": "规范", "path": "a.md",
			"title": "A", "file_name": "a.md", "start_line": 1, "end_line": 3,
		}}})
		writeJSONBody(w, map[string]any{
			"jsonrpc": "2.0", "id": 3,
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}},
		})
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *fakeOAuthProvider) serveRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RedirectURIs []string `json:"redirect_uris"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.registerCalls++
	f.mu.Unlock()
	if len(body.RedirectURIs) != 1 || !strings.HasSuffix(body.RedirectURIs[0], "/api/knowledge/oauth/mcp/callback") {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_redirect_uri"}`))
		return
	}
	writeJSONBody(w, map[string]any{
		"client_id":                  "dcr-client",
		"client_secret":              "dcr-secret",
		"token_endpoint_auth_method": "client_secret_basic",
	})
}

func (f *fakeOAuthProvider) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != "dcr-client" || pass != "dcr-secret" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != "code-1" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	challenge, resource, redirect := f.expectations()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"pkce mismatch"}`))
		return
	}
	if got := r.PostForm.Get("redirect_uri"); got != redirect {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"redirect mismatch"}`))
		return
	}
	if got := r.PostForm.Get("resource"); got != resource {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_target","error_description":"resource mismatch"}`))
		return
	}
	f.mu.Lock()
	f.tokenCalls++
	f.mu.Unlock()
	writeJSONBody(w, map[string]any{
		"access_token":  "at-1",
		"refresh_token": "rt-1",
		"expires_in":    3600,
		"token_type":    "Bearer",
	})
}

func TestMCPOAuthAutoFlowUsesDiscoveryDCRAndSession(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "knowledge-oauth-test-secret")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	provider := &fakeOAuthProvider{}
	srv := httptest.NewServer(provider)
	t.Cleanup(srv.Close)
	provider.setBaseURL(srv.URL)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "auto" })
	ctx := context.Background()

	endpoint := srv.URL + "/mcp?company_from=xxx"
	connector, err := svc.CreateConnector(ctx, 7, ConnectorInput{
		Name:     "团队 MCP",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: endpoint,
		Config: &domain.KnowledgeConnectorConfig{
			Extra: map[string]string{"oauthMode": OAuthModeAuto},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	authorizeURL, redirectURI, err := svc.StartOAuth(ctx, 7, connector.ID, "http://platform.example.com")
	if err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}
	if redirectURI != "http://platform.example.com/api/knowledge/oauth/mcp/callback" {
		t.Fatalf("unexpected redirect uri: %s", redirectURI)
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("authorize url: %v", err)
	}
	query := parsed.Query()
	if parsed.Path != "/authorize" {
		t.Fatalf("authorize path = %s", parsed.Path)
	}
	if got := query.Get("response_type"); got != "code" {
		t.Fatalf("response_type = %q", got)
	}
	if got := query.Get("client_id"); got != "dcr-client" {
		t.Fatalf("client_id = %q (DCR not applied)", got)
	}
	if got := query.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q", got)
	}
	challenge := query.Get("code_challenge")
	if challenge == "" {
		t.Fatal("authorize url misses code_challenge")
	}
	state := query.Get("state")
	if state == "" || len(state) >= 120 {
		t.Fatalf("state length = %d, want 1..119", len(state))
	}
	if got := query.Get("redirect_uri"); got != redirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, redirectURI)
	}
	if got := query.Get("resource"); got != srv.URL+"/mcp?company_from=xxx" {
		t.Fatalf("resource = %q (query not preserved)", got)
	}

	provider.setExpectations(challenge, srv.URL+"/mcp?company_from=xxx", redirectURI)

	if _, err := svc.CompleteOAuthCallback(ctx, "mcp", state, "code-1", ""); err != nil {
		t.Fatalf("CompleteOAuthCallback: %v", err)
	}

	saved, ok := store.GetKnowledgeConnector(7, connector.ID)
	if !ok {
		t.Fatal("connector missing after callback")
	}
	secret, err := OpenSecret(7, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatalf("open secret: %v", err)
	}
	if secret.Token != "at-1" || secret.RefreshToken != "rt-1" {
		t.Fatalf("tokens not sealed: %+v", secret)
	}
	if secret.OAuthClientID != "dcr-client" || secret.OAuthClientSecret != "dcr-secret" {
		t.Fatalf("client registration not stored: %+v", secret)
	}
	if secret.OAuthTokenURL != srv.URL+"/token" {
		t.Fatalf("token url = %q", secret.OAuthTokenURL)
	}
	if !strings.Contains(secret.OAuthResource, "company_from=xxx") {
		t.Fatalf("resource = %q", secret.OAuthResource)
	}
	expiry := parseSecretExpiry(secret.ExpiresAt)
	if expiry == nil || !expiry.After(time.Now()) {
		t.Fatalf("expiresAt = %q", secret.ExpiresAt)
	}

	_, runtime, err := svc.ResolveEnabledConnector(7, connector.ID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	result, err := runtime.CallTool(ctx, "kb_search", map[string]any{"query": "x"}, SourceScope{Mode: "all"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.Status != domain.KnowledgeStatusHit {
		t.Fatalf("status = %s", result.Status)
	}

	requests := provider.mcpRequests()
	sawInitialize, sawToolsList, sawCall := false, false, false
	for _, req := range requests {
		if req.Query != "company_from=xxx" {
			t.Fatalf("%s lost endpoint query: %q", req.Method, req.Query)
		}
		switch req.Method {
		case "initialize":
			sawInitialize = true
		case "tools/list":
			sawToolsList = true
			if req.Session != "sess-abc" {
				t.Fatalf("tools/list session = %q", req.Session)
			}
			if req.Auth != "Bearer at-1" {
				t.Fatalf("tools/list auth = %q", req.Auth)
			}
		case "notifications/initialized":
			if req.Session != "sess-abc" {
				t.Fatalf("notifications session = %q", req.Session)
			}
		case "tools/call":
			sawCall = true
			if req.Session != "sess-abc" {
				t.Fatalf("tools/call session = %q", req.Session)
			}
			if req.Auth != "Bearer at-1" {
				t.Fatalf("tools/call auth = %q", req.Auth)
			}
		}
	}
	if !sawInitialize || !sawToolsList || !sawCall {
		t.Fatalf("missing MCP calls: %+v", requests)
	}
	provider.mu.Lock()
	resourceHits, registrations, tokenCalls := provider.resourceMetaHits, provider.registerCalls, provider.tokenCalls
	provider.mu.Unlock()
	if resourceHits == 0 {
		t.Fatal("protected resource metadata was never fetched")
	}
	if registrations != 1 {
		t.Fatalf("DCR calls = %d, want 1", registrations)
	}
	if tokenCalls != 1 {
		t.Fatalf("token calls = %d, want 1", tokenCalls)
	}
}

// ---------------------------------------------------------------------------
// Case 2: Feishu-style refresh token rotation under 10 concurrent calls.
// ---------------------------------------------------------------------------

type feishuFake struct {
	mu            sync.Mutex
	tokenCalls    int
	refreshInputs []string
	mcpCalls      []string // X-Lark-MCP-UAT seen on tools/call
	mcpAuth       []string // Authorization seen on tools/call
	currentAT     string
}

func (f *feishuFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/token":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
			!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":99991,"msg":"json body required"}`))
			return
		}
		if body["grant_type"] != "refresh_token" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":99991,"msg":"grant"}`))
			return
		}
		refresh, _ := body["refresh_token"].(string)
		f.mu.Lock()
		f.tokenCalls++
		n := f.tokenCalls
		f.refreshInputs = append(f.refreshInputs, refresh)
		f.currentAT = fmt.Sprintf("at-%d", n)
		newRefresh := fmt.Sprintf("rt-%d", n)
		f.mu.Unlock()
		writeJSONBody(w, map[string]any{
			"code":          0,
			"access_token":  fmt.Sprintf("at-%d", n),
			"refresh_token": newRefresh,
			"expires_in":    7200,
		})
	case "/mcp":
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		want := f.currentAT
		if req.Method == "tools/call" {
			f.mcpCalls = append(f.mcpCalls, r.Header.Get("X-Lark-MCP-UAT"))
			f.mcpAuth = append(f.mcpAuth, r.Header.Get("Authorization"))
		}
		f.mu.Unlock()
		if r.Header.Get("X-Lark-MCP-UAT") != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "fs-session")
			writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []map[string]any{{
				"name": "kb_search", "description": "search", "inputSchema": map[string]any{"type": "object"},
				"annotations": map[string]any{"readOnlyHint": true},
			}}}})
		case "tools/call":
			text, _ := json.Marshal(map[string]any{"results": []map[string]any{{
				"source": "s1", "source_name": "飞书", "path": "a.md", "title": "A", "file_name": "a.md",
			}}})
			writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *feishuFake) snapshot() (int, []string, []string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls, append([]string(nil), f.refreshInputs...), append([]string(nil), f.mcpCalls...), append([]string(nil), f.mcpAuth...)
}

func TestMCPOAuthFeishuRefreshRotatesOnceUnderConcurrency(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "knowledge-oauth-feishu-secret")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	fake := &feishuFake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "fs" })
	ctx := context.Background()

	connector, err := svc.CreateConnector(ctx, 9, ConnectorInput{
		Name:     "飞书知识库",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp",
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":         OAuthModePreset,
			"oauthPreset":       "feishu",
			"oauthTokenHeader":  "X-Lark-MCP-UAT",
			"oauthTokenPrefix":  "",
			"oauthTokenUrl":     srv.URL + "/token",
			"oauthAuthorizeUrl": srv.URL + "/authorize",
		}},
		Secret: domain.KnowledgeConnectorSecret{
			Token:                "at-0",
			RefreshToken:         "rt-0",
			ExpiresAt:            time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339),
			OAuthTokenURL:        srv.URL + "/token",
			OAuthClientID:        "feishu-app",
			OAuthTokenAuthMethod: "json_body",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, runtime, err := svc.ResolveEnabledConnector(9, connector.ID)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 10
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			result, callErr := runtime.CallTool(ctx, "kb_search", map[string]any{"query": "x"}, SourceScope{Mode: "all"})
			if callErr != nil {
				errs <- callErr
				return
			}
			if result.Status != domain.KnowledgeStatusHit {
				errs <- fmt.Errorf("status = %s", result.Status)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for callErr := range errs {
		t.Fatalf("concurrent CallTool failed: %v", callErr)
	}

	tokenCalls, refreshInputs, uatHeaders, authHeaders := fake.snapshot()
	if tokenCalls != 1 {
		t.Fatalf("token endpoint calls = %d, want exactly 1 (refresh must be single-flight)", tokenCalls)
	}
	if len(refreshInputs) != 1 || refreshInputs[0] != "rt-0" {
		t.Fatalf("refresh inputs = %v", refreshInputs)
	}
	if len(uatHeaders) != workers {
		t.Fatalf("tools/call count = %d, want %d", len(uatHeaders), workers)
	}
	for i, got := range uatHeaders {
		if got != "at-1" {
			t.Fatalf("tools/call[%d] X-Lark-MCP-UAT = %q, want at-1", i, got)
		}
		if authHeaders[i] != "" {
			t.Fatalf("tools/call[%d] must not send Authorization, got %q", i, authHeaders[i])
		}
	}

	saved, ok := store.GetKnowledgeConnector(9, connector.ID)
	if !ok {
		t.Fatal("connector missing")
	}
	secret, err := OpenSecret(9, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if secret.Token != "at-1" || secret.RefreshToken != "rt-1" {
		t.Fatalf("rotated secret not persisted: token=%q refresh=%q", secret.Token, secret.RefreshToken)
	}
	firstExpiry := parseSecretExpiry(secret.ExpiresAt)
	if firstExpiry == nil || !firstExpiry.After(time.Now().Add(time.Hour)) {
		t.Fatalf("expiresAt = %q, want ~2h ahead", secret.ExpiresAt)
	}

	// Second refresh must consume rt-1, proving the rotation chain is correct.
	secret.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	sealed, err := SealSecret(9, saved.ID, secret)
	if err != nil {
		t.Fatal(err)
	}
	saved.SecretEnc = sealed
	if err := store.PutKnowledgeConnector(saved); err != nil {
		t.Fatal(err)
	}
	refreshed, err := svc.EnsureValidSecret(ctx, 9, connector.ID, false, "")
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if refreshed.Token != "at-2" || refreshed.RefreshToken != "rt-2" {
		t.Fatalf("second refresh = %+v, want at-2/rt-2", refreshed)
	}
	tokenCalls, refreshInputs, _, _ = fake.snapshot()
	if tokenCalls != 2 || refreshInputs[1] != "rt-1" {
		t.Fatalf("second refresh token calls=%d inputs=%v", tokenCalls, refreshInputs)
	}
	latest, _ := store.GetKnowledgeConnector(9, connector.ID)
	stored, err := OpenSecret(9, latest.ID, latest.SecretEnc)
	if err != nil || stored.RefreshToken != "rt-2" || stored.Token != "at-2" {
		t.Fatalf("second rotation not persisted: %+v %v", stored, err)
	}
}

func TestParseTokenResponseShapes(t *testing.T) {
	top, err := parseTokenResponse([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"scope":"a b"}`))
	if err != nil || top.AccessToken != "at" || top.RefreshToken != "rt" || top.ExpiresIn != 3600 {
		t.Fatalf("top-level = %+v, %v", top, err)
	}
	feishu, err := parseTokenResponse([]byte(`{"code":0,"data":{"access_token":"at2","refresh_token":"rt2","expires_in":7200}}`))
	if err != nil || feishu.AccessToken != "at2" || feishu.RefreshToken != "rt2" || feishu.ExpiresIn != 7200 {
		t.Fatalf("nested = %+v, %v", feishu, err)
	}
	if _, err := parseTokenResponse([]byte(`{"code":99991,"msg":"invalid refresh token"}`)); err == nil {
		t.Fatal("non-zero code must be an error")
	}
	if _, err := parseTokenResponse([]byte(`{"error":"invalid_grant","error_description":"expired"}`)); err == nil {
		t.Fatal("std error must be an error")
	}
	if _, err := parseTokenResponse([]byte(`{"code":0}`)); err == nil {
		t.Fatal("missing access_token must be an error")
	}
}

// ---------------------------------------------------------------------------
// Case 3: passive 401 refresh, redaction and completion page.
// ---------------------------------------------------------------------------

type refresh401Fake struct {
	mu         sync.Mutex
	tokenCalls int
	toolsCalls int
	callAuth   []string
	retryAuth  []string
}

func (f *refresh401Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/token":
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("grant_type") != "refresh_token" ||
			r.PostForm.Get("refresh_token") != "rt-1" ||
			r.PostForm.Get("client_id") != "cid" ||
			r.PostForm.Get("client_secret") != "csecret" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		f.mu.Lock()
		f.tokenCalls++
		f.mu.Unlock()
		writeJSONBody(w, map[string]any{"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600})
	case "/mcp":
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s401")
			writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []map[string]any{{
				"name": "kb_search", "description": "search", "inputSchema": map[string]any{"type": "object"},
				"annotations": map[string]any{"readOnlyHint": true},
			}}}})
		case "tools/call":
			f.mu.Lock()
			f.toolsCalls++
			attempt := f.toolsCalls
			auth := r.Header.Get("Authorization")
			if attempt == 1 {
				f.callAuth = append(f.callAuth, auth)
			} else {
				f.retryAuth = append(f.retryAuth, auth)
			}
			f.mu.Unlock()
			if attempt == 1 {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			text, _ := json.Marshal(map[string]any{"results": []map[string]any{{
				"source": "s1", "source_name": "规范", "path": "a.md", "title": "A", "file_name": "a.md",
			}}})
			writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestMCPOAuth401RefreshRetriesOnce(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "knowledge-oauth-401-secret")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	fake := &refresh401Fake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "r401" })
	ctx := context.Background()

	connector, err := svc.CreateConnector(ctx, 11, ConnectorInput{
		Name:     "401 MCP",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp",
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":         OAuthModeCustom,
			"oauthTokenUrl":     srv.URL + "/token",
			"oauthAuthorizeUrl": srv.URL + "/authorize",
		}},
		Secret: domain.KnowledgeConnectorSecret{
			Token:                "at-1",
			RefreshToken:         "rt-1",
			ExpiresAt:            time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			OAuthTokenURL:        srv.URL + "/token",
			OAuthClientID:        "cid",
			OAuthClientSecret:    "csecret",
			OAuthTokenAuthMethod: OAuthAuthMethodPost,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, runtime, err := svc.ResolveEnabledConnector(11, connector.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.CallTool(ctx, "kb_search", map[string]any{"query": "x"}, SourceScope{Mode: "all"})
	if err != nil {
		t.Fatalf("CallTool after forced refresh: %v", err)
	}
	if result.Status != domain.KnowledgeStatusHit {
		t.Fatalf("status = %s", result.Status)
	}

	fake.mu.Lock()
	tokenCalls, callAuth, retryAuth := fake.tokenCalls, append([]string(nil), fake.callAuth...), append([]string(nil), fake.retryAuth...)
	fake.mu.Unlock()
	if tokenCalls != 1 {
		t.Fatalf("token calls = %d, want 1", tokenCalls)
	}
	if len(callAuth) != 1 || callAuth[0] != "Bearer at-1" {
		t.Fatalf("first attempt auth = %v", callAuth)
	}
	if len(retryAuth) != 1 || retryAuth[0] != "Bearer at-2" {
		t.Fatalf("retry auth = %v", retryAuth)
	}

	saved, _ := store.GetKnowledgeConnector(11, connector.ID)
	secret, err := OpenSecret(11, saved.ID, saved.SecretEnc)
	if err != nil || secret.Token != "at-2" || secret.RefreshToken != "rt-2" {
		t.Fatalf("refreshed secret not persisted: %+v %v", secret, err)
	}
}

func TestOAuthSecretRedaction(t *testing.T) {
	secret := domain.KnowledgeConnectorSecret{
		Token:             "at-secret-value",
		RefreshToken:      "rt-secret-value",
		OAuthClientSecret: "cs-secret-value",
		OAuthClientID:     "public-client-id",
	}
	text := `Authorization: Bearer at-secret-value; refresh=rt-secret-value; client=cs-secret-value; access_token=at-secret-value; client_secret=cs-secret-value`
	redacted := RedactSecrets(text, secret)
	for _, leak := range []string{"at-secret-value", "rt-secret-value", "cs-secret-value"} {
		if strings.Contains(redacted, leak) {
			t.Fatalf("RedactSecrets leaked %q: %s", leak, redacted)
		}
	}

	err := oauthUpstreamError(errors.New("upstream rejected at-secret-value / rt-secret-value / cs-secret-value"), secret)
	for _, leak := range []string{"at-secret-value", "rt-secret-value", "cs-secret-value"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error path leaked %q: %s", leak, err.Error())
		}
	}

	httpErr := &mcpclient.HTTPError{
		StatusCode:      http.StatusUnauthorized,
		WWWAuthenticate: `Bearer leak-bearer`,
		Body:            `{"error":"invalid_token","access_token":"leak-at","refresh_token":"leak-rt","client_secret":"leak-cs"}`,
	}
	for _, leak := range []string{"leak-bearer", "leak-at", "leak-rt", "leak-cs"} {
		if strings.Contains(httpErr.Error(), leak) {
			t.Fatalf("HTTPError leaked %q: %s", leak, httpErr.Error())
		}
	}
}

// ---------------------------------------------------------------------------
// P0 regressions: connector-lock serialization, narrow secret writes and
// pending fingerprint binding.
// ---------------------------------------------------------------------------

// memoryConnectorStore implements the optional CAS contract of the production
// repo so service tests exercise the same narrow-update path.
func (m *memoryConnectorStore) UpdateKnowledgeConnectorSecret(_ context.Context, ownerUserID int64, id, expectedSecretEnc, secretEnc string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn, ok := m.items[id]
	if !ok || conn.OwnerUserID != ownerUserID {
		return false, nil
	}
	if conn.SecretEnc != expectedSecretEnc {
		return false, nil
	}
	conn.SecretEnc = secretEnc
	conn.SecretConfigured = secretEnc != ""
	conn.UpdatedAt = time.Now().UTC()
	m.items[id] = conn
	return true, nil
}

func (m *memoryConnectorStore) UpdateKnowledgeConnectorConfig(_ context.Context, ownerUserID int64, id, expectedConfigJSON, configJSON string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn, ok := m.items[id]
	if !ok || conn.OwnerUserID != ownerUserID {
		return false, nil
	}
	expectedBytes, err := json.Marshal(conn.Config)
	if err != nil || string(expectedBytes) != expectedConfigJSON {
		return false, nil
	}
	var cfg domain.KnowledgeConnectorConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return false, err
	}
	conn.Config = cfg
	conn.UpdatedAt = time.Now().UTC()
	m.items[id] = conn
	return true, nil
}

type rotationGateFake struct {
	mu             sync.Mutex
	tokenCalls     int
	refreshInputs  []string
	authCodeCalls  int
	refreshStarted chan struct{}
	authCodeSeen   chan struct{}
	releaseRefresh chan struct{}
	startOnce      sync.Once
	authOnce       sync.Once
}

func (f *rotationGateFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/token" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "refresh_token":
		f.mu.Lock()
		f.tokenCalls++
		f.refreshInputs = append(f.refreshInputs, r.PostForm.Get("refresh_token"))
		f.mu.Unlock()
		f.startOnce.Do(func() { close(f.refreshStarted) })
		<-f.releaseRefresh
		writeJSONBody(w, map[string]any{"access_token": "at-refreshed", "refresh_token": "rt-refreshed", "expires_in": 3600})
	case "authorization_code":
		f.mu.Lock()
		f.authCodeCalls++
		f.mu.Unlock()
		f.authOnce.Do(func() { close(f.authCodeSeen) })
		writeJSONBody(w, map[string]any{"access_token": "at-auth", "refresh_token": "rt-auth", "expires_in": 3600})
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (f *rotationGateFake) snapshot() (refreshCalls, authCalls int, inputs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls, f.authCodeCalls, append([]string(nil), f.refreshInputs...)
}

func newRotationGateFake() *rotationGateFake {
	return &rotationGateFake{
		refreshStarted: make(chan struct{}),
		authCodeSeen:   make(chan struct{}),
		releaseRefresh: make(chan struct{}),
	}
}

func seedExpiredOAuthConnector(t *testing.T, svc *Service, store *memoryConnectorStore, uid int64, endpoint, tokenURL string) domain.KnowledgeConnector {
	t.Helper()
	connector, err := svc.CreateConnector(context.Background(), uid, ConnectorInput{
		Name:     "P0 连接器",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: endpoint,
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":         OAuthModeCustom,
			"oauthTokenUrl":     tokenURL,
			"oauthAuthorizeUrl": tokenURL + "/authorize",
		}},
		Secret: domain.KnowledgeConnectorSecret{
			Token:                "at-old",
			RefreshToken:         "rt-old",
			ExpiresAt:            time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
			OAuthTokenURL:        tokenURL,
			OAuthClientID:        "cid",
			OAuthClientSecret:    "csecret",
			OAuthTokenAuthMethod: OAuthAuthMethodPost,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return connector
}

func authorizeState(t *testing.T, authorizeURL string) string {
	t.Helper()
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("authorize url misses state")
	}
	return state
}

// TestOAuthRefreshDoesNotClobberConcurrentAuthorization blocks a refresh in the
// token endpoint, completes a fresh authorization meanwhile, and asserts the
// authorization wins without the refresh writing its old rotation back.
func TestOAuthRefreshDoesNotClobberConcurrentAuthorization(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p0-refresh-vs-auth")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	fake := newRotationGateFake()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p0a" })
	ctx := context.Background()
	const uid = int64(21)
	connector := seedExpiredOAuthConnector(t, svc, store, uid, srv.URL+"/mcp", srv.URL+"/token")

	authorizeURL, _, err := svc.StartOAuth(ctx, uid, connector.ID, "http://platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizeState(t, authorizeURL)

	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := svc.EnsureValidSecret(ctx, uid, connector.ID, false, "")
		refreshDone <- refreshErr
	}()
	<-fake.refreshStarted

	callbackDone := make(chan error, 1)
	go func() {
		_, callbackErr := svc.CompleteOAuthCallback(ctx, "mcp", state, "code-1", "")
		callbackDone <- callbackErr
	}()
	<-fake.authCodeSeen
	close(fake.releaseRefresh)

	if err := <-refreshDone; err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := <-callbackDone; err != nil {
		t.Fatalf("callback: %v", err)
	}

	saved, ok := store.GetKnowledgeConnector(uid, connector.ID)
	if !ok {
		t.Fatal("connector missing")
	}
	secret, err := OpenSecret(uid, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if secret.Token != "at-auth" || secret.RefreshToken != "rt-auth" {
		t.Fatalf("final secret = %s/%s, want at-auth/rt-auth", secret.Token, secret.RefreshToken)
	}
	refreshCalls, authCalls, _ := fake.snapshot()
	if refreshCalls != 1 || authCalls != 1 {
		t.Fatalf("token calls refresh=%d auth=%d", refreshCalls, authCalls)
	}
}

// TestOAuthRefreshKeepsConcurrentConnectorEdits renames the connector and
// changes its endpoint while a refresh is in flight; the refresh must update
// only the sealed secret.
func TestOAuthRefreshKeepsConcurrentConnectorEdits(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p0-refresh-vs-edit")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	fake := newRotationGateFake()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p0b" })
	ctx := context.Background()
	const uid = int64(22)
	connector := seedExpiredOAuthConnector(t, svc, store, uid, srv.URL+"/mcp", srv.URL+"/token")

	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := svc.EnsureValidSecret(ctx, uid, connector.ID, false, "")
		refreshDone <- refreshErr
	}()
	<-fake.refreshStarted

	// Another process edits the record while the refresh waits on the token
	// endpoint (an in-process UpdateConnector now shares the connector lock and
	// would serialize, which is the P0 fix).
	newName := "用户改名后的连接器"
	newEndpoint := srv.URL + "/mcp?company_from=edited"
	latest, ok := store.GetKnowledgeConnector(uid, connector.ID)
	if !ok {
		t.Fatal("connector missing")
	}
	latest.Name = newName
	latest.Endpoint = newEndpoint
	if err := store.PutKnowledgeConnector(latest); err != nil {
		t.Fatalf("out-of-band edit during refresh: %v", err)
	}
	close(fake.releaseRefresh)
	if err := <-refreshDone; err != nil {
		t.Fatalf("refresh: %v", err)
	}

	saved, ok := store.GetKnowledgeConnector(uid, connector.ID)
	if !ok {
		t.Fatal("connector missing")
	}
	if saved.Name != newName || saved.Endpoint != newEndpoint {
		t.Fatalf("connector edits lost: name=%q endpoint=%q", saved.Name, saved.Endpoint)
	}
	secret, err := OpenSecret(uid, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if secret.Token != "at-refreshed" || secret.RefreshToken != "rt-refreshed" {
		t.Fatalf("refresh not persisted: %+v", secret)
	}
}

type tokenCountFake struct {
	mu            sync.Mutex
	authCodeCalls int
}

func (f *tokenCountFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/token" || r.ParseForm() != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.PostForm.Get("grant_type") == "authorization_code" {
		f.mu.Lock()
		f.authCodeCalls++
		f.mu.Unlock()
	}
	writeJSONBody(w, map[string]any{"access_token": "at-new", "refresh_token": "rt-new", "expires_in": 3600})
}

// TestOAuthCallbackRejectsChangedConnectorEndpoint changes the endpoint between
// StartOAuth and the callback: the callback must fail, write nothing and
// consume the pending authorization.
func TestOAuthCallbackRejectsChangedConnectorEndpoint(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p0-fingerprint")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	fake := &tokenCountFake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p0c" })
	ctx := context.Background()
	const uid = int64(23)
	connector := seedExpiredOAuthConnector(t, svc, store, uid, srv.URL+"/mcp-a", srv.URL+"/token")

	authorizeURL, _, err := svc.StartOAuth(ctx, uid, connector.ID, "http://platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizeState(t, authorizeURL)

	beforeEdit, _ := store.GetKnowledgeConnector(uid, connector.ID)
	editSecret, err := OpenSecret(uid, beforeEdit.ID, beforeEdit.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	// Re-entering the same secret is required when changing the endpoint; the
	// callback must still reject the edit through its authorization fingerprint.
	if _, err := svc.UpdateConnector(ctx, uid, connector.ID, ConnectorInput{Endpoint: srv.URL + "/mcp-b", Secret: editSecret}); err != nil {
		t.Fatalf("endpoint change: %v", err)
	}

	if _, err := svc.CompleteOAuthCallback(ctx, "mcp", state, "code-1", ""); err == nil ||
		!strings.Contains(err.Error(), "oauth connector changed during authorization") {
		t.Fatalf("callback err = %v, want connector-changed error", err)
	}
	fake.mu.Lock()
	authCodeCalls := fake.authCodeCalls
	fake.mu.Unlock()
	if authCodeCalls != 0 {
		t.Fatalf("token exchange must not run after a fingerprint mismatch, calls=%d", authCodeCalls)
	}
	saved, _ := store.GetKnowledgeConnector(uid, connector.ID)
	secret, err := OpenSecret(uid, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if secret.Token != "at-old" || secret.RefreshToken != "rt-old" {
		t.Fatalf("token written despite mismatch: %+v", secret)
	}
	if _, err := svc.CompleteOAuthCallback(ctx, "mcp", state, "code-1", ""); err == nil {
		t.Fatal("pending authorization must be consumed after a mismatch")
	}
}

// ---------------------------------------------------------------------------
// P1-1: DCR resume, omitted auth method fallback and AS metadata preference.
// ---------------------------------------------------------------------------

func TestOAuthDCRRegistrationSurvivesAbandonedCallback(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p1-dcr-resume")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	provider := &fakeOAuthProvider{}
	srv := httptest.NewServer(provider)
	t.Cleanup(srv.Close)
	provider.setBaseURL(srv.URL)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p1a" })
	ctx := context.Background()
	connector, err := svc.CreateConnector(ctx, 31, ConnectorInput{
		Name:     "DCR resume",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp?company_from=xxx",
		Config:   &domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthMode": OAuthModeAuto}},
	})
	if err != nil {
		t.Fatal(err)
	}

	firstURL, _, err := svc.StartOAuth(ctx, 31, connector.ID, "http://platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	registrations := provider.registerCalls
	provider.mu.Unlock()
	if registrations != 1 {
		t.Fatalf("DCR calls = %d, want 1", registrations)
	}

	// No callback happens (user abandons the tab). The registration must have
	// been sealed already so a second attempt reuses it.
	saved, _ := store.GetKnowledgeConnector(31, connector.ID)
	secret, err := OpenSecret(31, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if secret.OAuthClientID != "dcr-client" || secret.OAuthClientSecret != "dcr-secret" {
		t.Fatalf("registration not persisted: %+v", secret)
	}
	if secret.OAuthTokenURL != srv.URL+"/token" || secret.OAuthTokenAuthMethod != OAuthAuthMethodBasic {
		t.Fatalf("registration endpoints not persisted: %+v", secret)
	}

	secondURL, _, err := svc.StartOAuth(ctx, 31, connector.ID, "http://platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	registrations = provider.registerCalls
	provider.mu.Unlock()
	if registrations != 1 {
		t.Fatalf("second StartOAuth registered again: DCR calls = %d", registrations)
	}
	for _, raw := range []string{firstURL, secondURL} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := parsed.Query().Get("client_id"); got != "dcr-client" {
			t.Fatalf("client_id = %q", got)
		}
	}
}

type basicAuthFake struct {
	mu        sync.Mutex
	base      string
	basics    []string
	tokenURLs int
}

func (f *basicAuthFake) setBase(raw string) {
	f.mu.Lock()
	f.base = raw
	f.mu.Unlock()
}

func (f *basicAuthFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/mcp":
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.baseURL()+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
		writeJSONBody(w, map[string]any{
			"resource":              f.baseURL() + "/mcp",
			"authorization_servers": []string{f.baseURL()},
		})
	case "/.well-known/oauth-authorization-server":
		writeJSONBody(w, map[string]any{
			"issuer":                                f.baseURL(),
			"authorization_endpoint":                f.baseURL() + "/authorize",
			"token_endpoint":                        f.baseURL() + "/token",
			"registration_endpoint":                 f.baseURL() + "/register",
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
		})
	case "/register":
		// Deliberately omits token_endpoint_auth_method.
		writeJSONBody(w, map[string]any{"client_id": "basic-client", "client_secret": "basic-secret"})
	case "/token":
		user, pass, ok := r.BasicAuth()
		f.mu.Lock()
		f.tokenURLs++
		if ok {
			f.basics = append(f.basics, user+"|"+pass)
		}
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"basic required"}`))
			return
		}
		writeJSONBody(w, map[string]any{"access_token": "at-basic", "refresh_token": "rt-basic", "expires_in": 3600})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *basicAuthFake) baseURL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.base
}

func TestOAuthOmittedAuthMethodFallsBackToBasic(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p1-basic-fallback")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	fake := &basicAuthFake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	fake.setBase(srv.URL)

	store := newMemoryConnectorStore()
	next := 0
	svc := NewService(store, func() string { next++; return fmt.Sprintf("p1b%d", next) })
	ctx := context.Background()

	// (1) DCR response omits the auth method: RegisterClient must default to
	// client_secret_basic and the callback must use HTTP Basic.
	connector, err := svc.CreateConnector(ctx, 32, ConnectorInput{
		Name:     "basic fallback",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp",
		Config:   &domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthMode": OAuthModeAuto}},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizeURL, _, err := svc.StartOAuth(ctx, 32, connector.ID, "http://platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteOAuthCallback(ctx, "mcp", authorizeState(t, authorizeURL), "code-1", ""); err != nil {
		t.Fatalf("callback with omitted method: %v", err)
	}

	// (2) No DCR (preset client id): the method must come from the AS metadata,
	// which only advertises client_secret_basic.
	manual, err := svc.CreateConnector(ctx, 32, ConnectorInput{
		Name:     "manual client",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp",
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":     OAuthModeAuto,
			"oauthClientId": "manual-client",
		}},
		Secret: domain.KnowledgeConnectorSecret{OAuthClientSecret: "manual-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizeURL, _, err = svc.StartOAuth(ctx, 32, manual.ID, "http://platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteOAuthCallback(ctx, "mcp", authorizeState(t, authorizeURL), "code-1", ""); err != nil {
		t.Fatalf("callback with metadata-preferred method: %v", err)
	}

	fake.mu.Lock()
	basics, tokenURLs := append([]string(nil), fake.basics...), fake.tokenURLs
	fake.mu.Unlock()
	if tokenURLs != 2 {
		t.Fatalf("token calls = %d, want 2", tokenURLs)
	}
	want := []string{"basic-client|basic-secret", "manual-client|manual-secret"}
	for i, expected := range want {
		if i >= len(basics) || basics[i] != expected {
			t.Fatalf("basic auth #%d = %v, want %s", i, basics, expected)
		}
	}
}

// ---------------------------------------------------------------------------
// P1-2: environment proxies must not bypass the SSRF guard.
// ---------------------------------------------------------------------------

func TestOAuthClientIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("direct"))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, _, body, err := oauthFetch(ctx, http.MethodGet, srv.URL, "", nil, "", "")
	if err != nil || status != http.StatusOK || string(body) != "direct" {
		t.Fatalf("direct loopback fetch = %d %q, %v (proxy must be bypassed)", status, body, err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, _, err := oauthFetch(ctx, http.MethodGet, "http://192.168.255.1:9/blocked", "", nil, "", ""); err == nil ||
		!strings.Contains(err.Error(), "不允许访问") {
		t.Fatalf("private address must be blocked, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// P1-3: a late 401 must reuse an already refreshed token.
// ---------------------------------------------------------------------------

type late401Fake struct {
	mu            sync.Mutex
	oldCalls      int
	tokenCalls    int
	first401      chan struct{}
	secondHeld    chan struct{}
	refreshServed chan struct{}
	releaseFirst  chan struct{}
	releaseSecond chan struct{}
	firstOnce     sync.Once
	secondOnce    sync.Once
	refreshOnce   sync.Once
}

func newLate401Fake() *late401Fake {
	return &late401Fake{
		first401:      make(chan struct{}),
		secondHeld:    make(chan struct{}),
		refreshServed: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
}

func (f *late401Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/token":
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.tokenCalls++
		f.mu.Unlock()
		f.refreshOnce.Do(func() { close(f.refreshServed) })
		writeJSONBody(w, map[string]any{"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600})
	case "/mcp":
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "late-session")
			writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []map[string]any{{
				"name": "kb_search", "description": "search", "inputSchema": map[string]any{"type": "object"},
				"annotations": map[string]any{"readOnlyHint": true},
			}}}})
		case "tools/call":
			if r.Header.Get("Authorization") == "Bearer at-1" {
				f.mu.Lock()
				f.oldCalls++
				attempt := f.oldCalls
				f.mu.Unlock()
				if attempt == 1 {
					f.firstOnce.Do(func() { close(f.first401) })
					<-f.releaseFirst
				} else {
					f.secondOnce.Do(func() { close(f.secondHeld) })
					<-f.releaseSecond
				}
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			text, _ := json.Marshal(map[string]any{"results": []map[string]any{{
				"source": "s1", "source_name": "规范", "path": "a.md", "title": "A", "file_name": "a.md",
			}}})
			writeJSONBody(w, map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *late401Fake) snapshot() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls
}

func TestOAuthLate401ReusesRefreshedToken(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p1-late-401")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	fake := newLate401Fake()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p1c" })
	ctx := context.Background()
	connector, err := svc.CreateConnector(ctx, 33, ConnectorInput{
		Name:     "late 401",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp",
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":         OAuthModeCustom,
			"oauthTokenUrl":     srv.URL + "/token",
			"oauthAuthorizeUrl": srv.URL + "/authorize",
		}},
		Secret: domain.KnowledgeConnectorSecret{
			Token:                "at-1",
			RefreshToken:         "rt-1",
			ExpiresAt:            time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			OAuthTokenURL:        srv.URL + "/token",
			OAuthClientID:        "cid",
			OAuthClientSecret:    "csecret",
			OAuthTokenAuthMethod: OAuthAuthMethodPost,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, runtime, err := svc.ResolveEnabledConnector(33, connector.ID)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, callErr := runtime.CallTool(ctx, "kb_search", map[string]any{"query": "x"}, SourceScope{Mode: "all"})
			results <- callErr
		}()
	}
	close(start)

	<-fake.first401
	<-fake.secondHeld
	close(fake.releaseFirst)
	<-fake.refreshServed
	if err := <-results; err != nil {
		t.Fatalf("first caller: %v", err)
	}
	close(fake.releaseSecond)
	if err := <-results; err != nil {
		t.Fatalf("late 401 caller: %v", err)
	}
	if calls := fake.snapshot(); calls != 1 {
		t.Fatalf("token calls = %d, want exactly 1 (late 401 must reuse at-2)", calls)
	}
	saved, _ := store.GetKnowledgeConnector(33, connector.ID)
	secret, err := OpenSecret(33, saved.ID, saved.SecretEnc)
	if err != nil || secret.Token != "at-2" || secret.RefreshToken != "rt-2" {
		t.Fatalf("stored secret = %+v, %v", secret, err)
	}
}

// ---------------------------------------------------------------------------
// P1-4: discovery cache must not leak across endpoints.
// ---------------------------------------------------------------------------

func TestOAuthDiscoveryCacheFollowsEndpointChange(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p1-discovery-cache")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	providerA := &fakeOAuthProvider{}
	srvA := httptest.NewServer(providerA)
	t.Cleanup(srvA.Close)
	providerA.setBaseURL(srvA.URL)

	providerB := &fakeOAuthProvider{}
	srvB := httptest.NewServer(providerB)
	t.Cleanup(srvB.Close)
	providerB.setBaseURL(srvB.URL)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p1d" })
	ctx := context.Background()
	connector, err := svc.CreateConnector(ctx, 34, ConnectorInput{
		Name:     "cache move",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srvA.URL + "/mcp",
		Config:   &domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthMode": OAuthModeAuto}},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstURL, _, err := svc.StartOAuth(ctx, 34, connector.ID, "http://platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if parsed, _ := url.Parse(firstURL); parsed == nil || parsed.Host != strings.TrimPrefix(srvA.URL, "http://") {
		t.Fatalf("first authorize url = %s", firstURL)
	}
	providerA.mu.Lock()
	aHits, aRegs := providerA.resourceMetaHits, providerA.registerCalls
	providerA.mu.Unlock()

	// Endpoint changes must re-supply or clear credentials (P1-3); OAuth auto
	// mode has no static secret, so clear the old sealed credential explicitly.
	if _, err := svc.UpdateConnector(ctx, 34, connector.ID, ConnectorInput{Endpoint: srvB.URL + "/mcp", ClearSecret: true}); err != nil {
		t.Fatalf("update endpoint: %v", err)
	}
	secondURL, _, err := svc.StartOAuth(ctx, 34, connector.ID, "http://platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(secondURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != strings.TrimPrefix(srvB.URL, "http://") {
		t.Fatalf("second authorize host = %s, want B (%s)", parsed.Host, srvB.URL)
	}
	providerA.mu.Lock()
	aHitsAfter, aRegsAfter := providerA.resourceMetaHits, providerA.registerCalls
	providerA.mu.Unlock()
	if aHitsAfter != aHits || aRegsAfter != aRegs {
		t.Fatalf("endpoint A was consulted again: hits %d→%d regs %d→%d", aHits, aHitsAfter, aRegs, aRegsAfter)
	}
	providerB.mu.Lock()
	bHits := providerB.resourceMetaHits
	providerB.mu.Unlock()
	if bHits == 0 {
		t.Fatal("endpoint B discovery was never consulted")
	}
}

// ---------------------------------------------------------------------------
// P1-5: entropy failures must fail closed and leave no pending authorization.
// ---------------------------------------------------------------------------

type failingEntropyReader struct{}

func (failingEntropyReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func TestOAuthStartFailsClosedWithoutEntropy(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p1-entropy")
	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p1e" })
	ctx := context.Background()
	const uid = int64(41)

	connector, err := svc.CreateConnector(ctx, uid, ConnectorInput{
		Name:     "entropy",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: "http://127.0.0.1:1/mcp",
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":         OAuthModeCustom,
			"oauthTokenUrl":     "http://127.0.0.1:1/token",
			"oauthAuthorizeUrl": "http://127.0.0.1:1/authorize",
			"oauthClientId":     "cid",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	original := oauthRandReader
	oauthRandReader = failingEntropyReader{}
	t.Cleanup(func() { oauthRandReader = original })

	if _, _, err := svc.StartOAuth(ctx, uid, connector.ID, "http://platform.example.com"); err == nil {
		t.Fatal("StartOAuth must fail when the entropy source fails")
	}
	svc.oauthMu.Lock()
	pending := len(svc.oauthPending)
	svc.oauthMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending entries = %d, want 0 after entropy failure", pending)
	}

	oauthRandReader = original
	if _, _, err := svc.StartOAuth(ctx, uid, connector.ID, "http://platform.example.com"); err != nil {
		t.Fatalf("StartOAuth after entropy recovery: %v", err)
	}
}

// ---------------------------------------------------------------------------
// P1-6: callback failures expose a fixed code and redact echoed credentials.
// ---------------------------------------------------------------------------

type echoingTokenFake struct {
	mu           sync.Mutex
	lastVerifier string
	lastCode     string
}

func (f *echoingTokenFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/token" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = r.ParseForm()
	f.mu.Lock()
	f.lastVerifier = r.PostForm.Get("code_verifier")
	f.lastCode = r.PostForm.Get("code")
	f.mu.Unlock()
	w.WriteHeader(http.StatusBadRequest)
	writeJSONBody(w, map[string]any{
		"error":             "invalid_grant",
		"error_description": "rejected secret cs-echo-secret code " + r.PostForm.Get("code") + " verifier " + r.PostForm.Get("code_verifier"),
	})
}

func TestOAuthCallbackRedactsEchoedSecrets(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p1-redact")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	fake := &echoingTokenFake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p1f" })
	ctx := context.Background()
	const uid = int64(42)
	connector, err := svc.CreateConnector(ctx, uid, ConnectorInput{
		Name:     "redact",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp",
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":         OAuthModeCustom,
			"oauthTokenUrl":     srv.URL + "/token",
			"oauthAuthorizeUrl": srv.URL + "/authorize",
			"oauthClientId":     "cid",
		}},
		Secret: domain.KnowledgeConnectorSecret{OAuthClientSecret: "cs-echo-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorizeURL, _, err := svc.StartOAuth(ctx, uid, connector.ID, "http://platform.example.com")
	if err != nil {
		t.Fatal(err)
	}
	const authCode = "code-echo-123"
	_, err = svc.CompleteOAuthCallback(ctx, "mcp", authorizeState(t, authorizeURL), authCode, "")
	if err == nil {
		t.Fatal("callback must fail when the token endpoint rejects it")
	}
	fake.mu.Lock()
	verifier := fake.lastVerifier
	fake.mu.Unlock()
	if verifier == "" {
		t.Fatal("test did not observe the code_verifier")
	}
	message := err.Error()
	for _, leak := range []string{"cs-echo-secret", authCode, verifier} {
		if strings.Contains(message, leak) {
			t.Fatalf("error leaked %q: %s", leak, message)
		}
	}
	var callbackErr *OAuthCallbackError
	if !errors.As(err, &callbackErr) || callbackErr.Code != OAuthCallbackCodeFailed {
		t.Fatalf("callback error code = %+v", callbackErr)
	}
}

// ---------------------------------------------------------------------------
// Closing item A: config writes are narrow and survive concurrent renames.
// ---------------------------------------------------------------------------

type gatedConfigStore struct {
	*memoryConnectorStore
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedConfigStore) UpdateKnowledgeConnectorConfig(ctx context.Context, ownerUserID int64, id, expectedConfigJSON, configJSON string) (bool, error) {
	g.once.Do(func() { close(g.reached) })
	<-g.release
	return g.memoryConnectorStore.UpdateKnowledgeConnectorConfig(ctx, ownerUserID, id, expectedConfigJSON, configJSON)
}

func TestOAuthStartConfigWriteKeepsConcurrentRename(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p1-config-cas")
	store := &gatedConfigStore{
		memoryConnectorStore: newMemoryConnectorStore(),
		reached:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	svc := NewService(store, func() string { return "p1g" })
	ctx := context.Background()
	const uid = int64(43)
	connector, err := svc.CreateConnector(ctx, uid, ConnectorInput{
		Name:     "旧名称",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: "http://127.0.0.1:1/mcp",
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":         OAuthModeCustom,
			"oauthTokenUrl":     "http://127.0.0.1:1/token",
			"oauthAuthorizeUrl": "http://127.0.0.1:1/authorize",
		}},
		// The client id lives in the sealed secret only, so StartOAuth must add
		// oauthClientId to config_json and hit the config CAS.
		Secret: domain.KnowledgeConnectorSecret{OAuthClientID: "cid", OAuthClientSecret: "csecret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	startDone := make(chan error, 1)
	go func() {
		_, _, startErr := svc.StartOAuth(ctx, uid, connector.ID, "http://platform.example.com")
		startDone <- startErr
	}()
	select {
	case <-store.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("StartOAuth never reached the config CAS")
	}

	// Simulate another process renaming the connector while the config CAS is
	// in flight (an in-process UpdateConnector now shares the connector lock and
	// would wait, which is exactly the P0 fix).
	newName := "并发改名"
	latest, ok := store.GetKnowledgeConnector(uid, connector.ID)
	if !ok {
		t.Fatal("connector missing")
	}
	latest.Name = newName
	if err := store.PutKnowledgeConnector(latest); err != nil {
		t.Fatalf("out-of-band rename: %v", err)
	}
	close(store.release)
	if err := <-startDone; err != nil {
		t.Fatalf("StartOAuth: %v", err)
	}

	saved, ok := store.GetKnowledgeConnector(uid, connector.ID)
	if !ok {
		t.Fatal("connector missing")
	}
	if saved.Name != newName || saved.Endpoint != connector.Endpoint {
		t.Fatalf("rename lost: name=%q endpoint=%q", saved.Name, saved.Endpoint)
	}
	if saved.Config.Extra["oauthTokenUrl"] == "" {
		t.Fatalf("OAuth config not persisted: %+v", saved.Config.Extra)
	}
}

// ---------------------------------------------------------------------------
// P0: StartOAuth must not rebase a registration onto a changed connector.
// ---------------------------------------------------------------------------

type gatedRegisterProvider struct {
	*fakeOAuthProvider
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedRegisterProvider() *gatedRegisterProvider {
	return &gatedRegisterProvider{
		fakeOAuthProvider: &fakeOAuthProvider{},
		started:           make(chan struct{}),
		release:           make(chan struct{}),
	}
}

func (g *gatedRegisterProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/register" {
		g.once.Do(func() { close(g.started) })
		<-g.release
	}
	g.fakeOAuthProvider.ServeHTTP(w, r)
}

func TestOAuthStartDoesNotRebaseRegistrationOntoChangedEndpoint(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p0-rebase-endpoint")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	provider := newGatedRegisterProvider()
	srv := httptest.NewServer(provider)
	t.Cleanup(srv.Close)
	provider.setBaseURL(srv.URL)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p0r" })
	ctx := context.Background()
	const uid = int64(51)
	connector, err := svc.CreateConnector(ctx, uid, ConnectorInput{
		Name:     "rebase",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp",
		Config:   &domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthMode": OAuthModeAuto}},
	})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan error, 1)
	go func() {
		_, _, startErr := svc.StartOAuth(ctx, uid, connector.ID, "http://platform.example.com")
		started <- startErr
	}()
	<-provider.started

	if _, err := svc.UpdateConnector(ctx, uid, connector.ID, ConnectorInput{Endpoint: srv.URL + "/mcp?changed=1", ClearSecret: true}); err != nil {
		t.Fatalf("endpoint change during DCR: %v", err)
	}
	close(provider.release)
	if err := <-started; err == nil || !strings.Contains(err.Error(), "oauth connector changed during authorization") {
		t.Fatalf("StartOAuth err = %v, want connector-changed", err)
	}

	saved, ok := store.GetKnowledgeConnector(uid, connector.ID)
	if !ok {
		t.Fatal("connector missing")
	}
	secret, err := OpenSecret(uid, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if secret.OAuthClientID != "" || secret.OAuthClientSecret != "" {
		t.Fatalf("registration sealed onto changed connector: %+v", secret)
	}
	for _, key := range []string{"oauthAuthorizeUrl", "oauthTokenUrl", "oauthClientId"} {
		if value := saved.Config.Extra[key]; value != "" {
			t.Fatalf("config key %s persisted on changed connector: %q", key, value)
		}
	}
	svc.oauthMu.Lock()
	pending := len(svc.oauthPending)
	svc.oauthMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending entries = %d, want 0", pending)
	}
}

func TestOAuthStartAllowsConcurrentRenameDuringDCR(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p0-rebase-rename")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	provider := newGatedRegisterProvider()
	srv := httptest.NewServer(provider)
	t.Cleanup(srv.Close)
	provider.setBaseURL(srv.URL)

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "p0rn" })
	ctx := context.Background()
	const uid = int64(52)
	connector, err := svc.CreateConnector(ctx, uid, ConnectorInput{
		Name:     "旧名",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp",
		Config:   &domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthMode": OAuthModeAuto}},
	})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan error, 1)
	go func() {
		_, _, startErr := svc.StartOAuth(ctx, uid, connector.ID, "http://platform.example.com")
		started <- startErr
	}()
	<-provider.started

	newName := "授权期间改名"
	if _, err := svc.UpdateConnector(ctx, uid, connector.ID, ConnectorInput{Name: newName}); err != nil {
		t.Fatalf("rename during DCR: %v", err)
	}
	close(provider.release)
	if err := <-started; err != nil {
		t.Fatalf("StartOAuth must survive a pure rename: %v", err)
	}

	saved, ok := store.GetKnowledgeConnector(uid, connector.ID)
	if !ok {
		t.Fatal("connector missing")
	}
	if saved.Name != newName {
		t.Fatalf("rename lost: %q", saved.Name)
	}
	secret, err := OpenSecret(uid, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	if secret.OAuthClientID != "dcr-client" || secret.OAuthClientSecret != "dcr-secret" {
		t.Fatalf("registration not persisted: %+v", secret)
	}
	if saved.Config.Extra["oauthTokenUrl"] != srv.URL+"/token" {
		t.Fatalf("oauth config not persisted: %+v", saved.Config.Extra)
	}
}

// ---------------------------------------------------------------------------
// P1/P2: EnsureValidSecret double-check semantics while waiting for the lock.
// ---------------------------------------------------------------------------

type signalGetStore struct {
	*memoryConnectorStore
	mu   sync.Mutex
	got  chan struct{}
	once *sync.Once
}

func newSignalGetStore() *signalGetStore {
	return &signalGetStore{memoryConnectorStore: newMemoryConnectorStore()}
}

func (s *signalGetStore) arm() {
	s.mu.Lock()
	s.got = make(chan struct{})
	s.once = &sync.Once{}
	s.mu.Unlock()
}

func (s *signalGetStore) GetKnowledgeConnector(uid int64, id string) (domain.KnowledgeConnector, bool) {
	conn, ok := s.memoryConnectorStore.GetKnowledgeConnector(uid, id)
	s.mu.Lock()
	got, once := s.got, s.once
	s.mu.Unlock()
	if got != nil && once != nil {
		once.Do(func() { close(got) })
	}
	return conn, ok
}

func (s *signalGetStore) firstRead() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got
}

type countingRefreshFake struct {
	mu    sync.Mutex
	calls int
}

func (f *countingRefreshFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	writeJSONBody(w, map[string]any{"access_token": "at-refreshed", "refresh_token": "rt-refreshed", "expires_in": 3600})
}

func TestOAuthEnsureValidSecretRefreshesExpiredTokenWrittenWhileWaiting(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p1-expired-while-waiting")
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = false })

	fake := &countingRefreshFake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	store := newSignalGetStore()
	svc := NewService(store, func() string { return "p1w" })
	ctx := context.Background()
	const uid = int64(53)
	connector, err := svc.CreateConnector(ctx, uid, ConnectorInput{
		Name:     "expired race",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL + "/mcp",
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":         OAuthModeCustom,
			"oauthTokenUrl":     srv.URL + "/token",
			"oauthAuthorizeUrl": srv.URL + "/authorize",
		}},
		Secret: domain.KnowledgeConnectorSecret{
			Token:                "token-A",
			RefreshToken:         "rt-A",
			ExpiresAt:            time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
			OAuthTokenURL:        srv.URL + "/token",
			OAuthClientID:        "cid",
			OAuthClientSecret:    "csecret",
			OAuthTokenAuthMethod: OAuthAuthMethodPost,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	lockKey := instanceKey(uid, connector.ID)
	held := svc.acquireOAuthLock(lockKey)
	store.arm()

	type outcome struct {
		secret domain.KnowledgeConnectorSecret
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		secret, err := svc.EnsureValidSecret(ctx, uid, connector.ID, false, "")
		done <- outcome{secret: secret, err: err}
	}()
	<-store.firstRead()

	// Another writer stores a different but ALREADY EXPIRED token while the
	// caller waits for the connector lock.
	saved, _ := store.GetKnowledgeConnector(uid, connector.ID)
	other, err := OpenSecret(uid, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	other.Token = "token-B"
	other.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	sealed, err := SealSecret(uid, saved.ID, other)
	if err != nil {
		t.Fatal(err)
	}
	saved.SecretEnc = sealed
	if err := store.PutKnowledgeConnector(saved); err != nil {
		t.Fatal(err)
	}
	svc.releaseOAuthLock(lockKey, held)

	result := <-done
	if result.err != nil {
		t.Fatalf("EnsureValidSecret: %v", result.err)
	}
	if result.secret.Token == "token-B" {
		t.Fatal("expired token written while waiting was reused without refresh")
	}
	if result.secret.Token != "at-refreshed" {
		t.Fatalf("token = %q", result.secret.Token)
	}
	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", calls)
	}
}

func TestOAuthEnsureValidSecretReusesAuthorizationWrittenWhileWaiting(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "p2-waiting-no-refresh")
	store := newSignalGetStore()
	svc := NewService(store, func() string { return "p2w" })
	ctx := context.Background()
	const uid = int64(54)
	connector, err := svc.CreateConnector(ctx, uid, ConnectorInput{
		Name:     "no refresh",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: "http://127.0.0.1:1/mcp",
		Config: &domain.KnowledgeConnectorConfig{Extra: map[string]string{
			"oauthMode":         OAuthModeCustom,
			"oauthTokenUrl":     "http://127.0.0.1:1/token",
			"oauthAuthorizeUrl": "http://127.0.0.1:1/authorize",
		}},
		Secret: domain.KnowledgeConnectorSecret{
			Token:             "token-A",
			ExpiresAt:         time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
			OAuthTokenURL:     "http://127.0.0.1:1/token",
			OAuthClientID:     "cid",
			OAuthClientSecret: "csecret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	lockKey := instanceKey(uid, connector.ID)
	held := svc.acquireOAuthLock(lockKey)
	store.arm()

	type outcome struct {
		secret domain.KnowledgeConnectorSecret
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		secret, err := svc.EnsureValidSecret(ctx, uid, connector.ID, false, "")
		done <- outcome{secret: secret, err: err}
	}()
	<-store.firstRead()

	// Another caller completes the authorization (fresh token, no refresh token)
	// while we are waiting for the lock.
	saved, _ := store.GetKnowledgeConnector(uid, connector.ID)
	other, err := OpenSecret(uid, saved.ID, saved.SecretEnc)
	if err != nil {
		t.Fatal(err)
	}
	other.Token = "token-authorized"
	other.RefreshToken = ""
	other.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	sealed, err := SealSecret(uid, saved.ID, other)
	if err != nil {
		t.Fatal(err)
	}
	saved.SecretEnc = sealed
	if err := store.PutKnowledgeConnector(saved); err != nil {
		t.Fatal(err)
	}
	svc.releaseOAuthLock(lockKey, held)

	result := <-done
	if result.err != nil {
		t.Fatalf("EnsureValidSecret must reuse the fresh authorization, got %v", result.err)
	}
	if result.secret.Token != "token-authorized" {
		t.Fatalf("token = %q, want token-authorized", result.secret.Token)
	}
}

// ---------------------------------------------------------------------------
// P2: connector lock lifecycle.
// ---------------------------------------------------------------------------

func TestOAuthConnectorLockLifecycleAndSweep(t *testing.T) {
	svc := NewService(newMemoryConnectorStore(), func() string { return "lock" })
	key := instanceKey(1, "conn")
	entry := svc.acquireOAuthLock(key)

	// Held lock (refs=1) must never be swept.
	svc.sweepOAuthLock(key)
	if _, ok := svc.oauthLocks.Load(key); !ok {
		t.Fatal("sweep deleted a held lock entry")
	}

	waiterDone := make(chan struct{})
	go func() {
		waiter := svc.acquireOAuthLock(key)
		svc.releaseOAuthLock(key, waiter)
		close(waiterDone)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		svc.oauthLocksMu.Lock()
		refs := 0
		if value, ok := svc.oauthLocks.Load(key); ok {
			refs = value.(*oauthLockEntry).refs
		}
		svc.oauthLocksMu.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiter never registered, refs=%d", refs)
		}
		time.Sleep(time.Millisecond)
	}
	// Holder + waiter: sweep must still keep the entry.
	svc.sweepOAuthLock(key)
	if _, ok := svc.oauthLocks.Load(key); !ok {
		t.Fatal("sweep deleted an entry with a waiter")
	}

	svc.releaseOAuthLock(key, entry)
	<-waiterDone
	if _, ok := svc.oauthLocks.Load(key); ok {
		t.Fatal("lock entry leaked after the last release")
	}
}

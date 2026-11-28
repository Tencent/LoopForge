package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

type casFailStore struct {
	*memoryConnectorStore
	failSecretCAS atomic.Int32
	failConfigCAS atomic.Int32
}

func (s *casFailStore) UpdateKnowledgeConnectorSecret(ctx context.Context, uid int64, id, expected, sealed string) (bool, error) {
	if s.failSecretCAS.Load() > 0 {
		s.failSecretCAS.Add(-1)
		return false, nil
	}
	latest, ok := s.GetKnowledgeConnector(uid, id)
	if !ok || latest.SecretEnc != expected {
		return false, nil
	}
	latest.SecretEnc = sealed
	latest.SecretConfigured = sealed != ""
	if err := s.PutKnowledgeConnector(latest); err != nil {
		return false, err
	}
	return true, nil
}

func (s *casFailStore) UpdateKnowledgeConnectorConfig(ctx context.Context, uid int64, id, expected, current string) (bool, error) {
	if s.failConfigCAS.Load() > 0 {
		s.failConfigCAS.Add(-1)
		return false, nil
	}
	latest, ok := s.GetKnowledgeConnector(uid, id)
	if !ok {
		return false, nil
	}
	b, _ := json.Marshal(latest.Config)
	if string(b) != expected {
		return false, nil
	}
	var cfg domain.KnowledgeConnectorConfig
	if err := json.Unmarshal([]byte(current), &cfg); err != nil {
		return false, err
	}
	latest.Config = cfg
	if err := s.PutKnowledgeConnector(latest); err != nil {
		return false, err
	}
	return true, nil
}

func TestOAuthDiscoveryHelpersAndFallbacks(t *testing.T) {
	orig := oauthAllowLoopbackForTest
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = orig })

	// parseAuthParam edge cases.
	hdr := `Bearer realm="example, inc", error="bad \"quote\"", resource_metadata=https://a.example/rm, xresource_metadata=https://bad`
	if got := parseAuthParam(hdr, "realm"); got != "example, inc" {
		t.Fatalf("expected quoted comma value, got %q", got)
	}
	if got := parseAuthParam(hdr, "error"); got != `bad "quote"` {
		t.Fatalf("expected unescaped quote, got %q", got)
	}
	if got := parseAuthParam(hdr, "resource_metadata"); got != "https://a.example/rm" {
		t.Fatalf("expected exact boundary match, got %q", got)
	}
	if got := parseAuthParam("xresource_metadata=1", "resource_metadata"); got != "" {
		t.Fatalf("expected prefix boundary rejection, got %q", got)
	}
	if got := parseAuthParam("", "realm"); got != "" {
		t.Fatalf("expected empty for empty header, got %q", got)
	}

	// canonicalResource.
	if got := canonicalResource("HTTPS://User:Pass@MCP.Example.COM?k=1#frag"); got != "https://mcp.example.com/?k=1" {
		t.Fatalf("unexpected canonicalResource: %q", got)
	}
	if got := canonicalResource("not-a-url"); got != "not-a-url" {
		t.Fatalf("unexpected invalid canonicalResource: %q", got)
	}

	// Local discovery server covering header fallback, well-known, OIDC fallback, probe, DCR.
	var mode atomic.Int32
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			if mode.Load() == 1 {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+srvURL+`/rm-bad"`)
			w.WriteHeader(http.StatusUnauthorized)
		case "/rm-bad":
			w.WriteHeader(http.StatusNotFound)
		case "/rm-good":
			writeJSONBody(w, map[string]any{"authorization_servers": []string{srvURL + "/auth"}})
		case "/.well-known/oauth-protected-resource/mcp":
			if mode.Load() == 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSONBody(w, map[string]any{"authorization_servers": []string{srvURL}})
		case "/.well-known/oauth-protected-resource":
			w.WriteHeader(http.StatusNotFound)
		case "/.well-known/oauth-authorization-server":
			w.WriteHeader(http.StatusNotFound)
		case "/.well-known/openid-configuration":
			if mode.Load() == 3 {
				writeJSONBody(w, map[string]any{"authorization_endpoint": "javascript:alert(1)", "token_endpoint": srvURL + "/token"})
				return
			}
			if mode.Load() == 4 {
				writeJSONBody(w, map[string]any{"authorization_endpoint": srvURL + "/authz"}) // missing token_endpoint
				return
			}
			writeJSONBody(w, map[string]any{
				"authorization_endpoint": srvURL + "/authz",
				"token_endpoint":         srvURL + "/token",
			})
		case "/register":
			switch mode.Load() {
			case 5:
				w.WriteHeader(http.StatusBadRequest)
				writeJSONBody(w, map[string]any{"error_description": "redirect_uri rejected"})
			case 6:
				w.WriteHeader(http.StatusInternalServerError)
			case 7:
				_, _ = w.Write([]byte("{bad-json"))
			case 8:
				writeJSONBody(w, map[string]any{"error": "invalid_client_metadata"})
			case 9:
				writeJSONBody(w, map[string]any{"client_secret": "only-secret"})
			default:
				writeJSONBody(w, map[string]any{"client_id": "dcr-id-1"})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	ctx := context.Background()
	if _, err := DiscoverProtectedResource(ctx, "not-valid", ""); err == nil {
		t.Fatal("expected error for invalid MCP URL")
	}
	// Header resource_metadata points to 200 JSON.
	rmHdr, err := DiscoverProtectedResource(ctx, srvURL+"/mcp", `Bearer resource_metadata="`+srvURL+`/rm-good"`)
	if err != nil || rmHdr.AuthorizationServers[0] != srvURL+"/auth" {
		t.Fatalf("expected header resource_metadata win, got %+v err=%v", rmHdr, err)
	}
	// Bad header falls back to well-known.
	rmWK, err := DiscoverProtectedResource(ctx, srvURL+"/mcp", `Bearer resource_metadata="`+srvURL+`/rm-bad"`)
	if err != nil || rmWK.AuthorizationServers[0] != srvURL {
		t.Fatalf("expected well-known fallback, got %+v err=%v", rmWK, err)
	}
	// No well-known falls back to endpoint origin.
	mode.Store(2)
	rmOrigin, err := DiscoverProtectedResource(ctx, srvURL+"/mcp", "")
	if err != nil || rmOrigin.AuthorizationServers[0] != srvURL {
		t.Fatalf("expected origin fallback, got %+v err=%v", rmOrigin, err)
	}
	mode.Store(0)

	// DiscoverAuthServer OIDC fallback and validation errors.
	if _, err := DiscoverAuthServer(ctx, "bad"); err == nil {
		t.Fatal("expected error for invalid issuer")
	}
	asMeta, err := DiscoverAuthServer(ctx, srvURL)
	if err != nil || asMeta.Issuer != srvURL || asMeta.TokenEndpoint != srvURL+"/token" {
		t.Fatalf("expected OIDC fallback metadata, got %+v err=%v", asMeta, err)
	}
	mode.Store(3)
	if _, err := DiscoverAuthServer(ctx, srvURL); err == nil || !strings.Contains(err.Error(), "http/https") {
		t.Fatalf("expected non-http endpoint rejection, got %v", err)
	}
	mode.Store(4)
	if _, err := DiscoverAuthServer(ctx, srvURL); err == nil || !strings.Contains(err.Error(), "缺少 endpoint") {
		t.Fatalf("expected missing endpoint rejection, got %v", err)
	}
	mode.Store(0)

	// ProbeWWWAuthenticate.
	if got := ProbeWWWAuthenticate(ctx, srvURL+"/mcp"); !strings.Contains(got, "resource_metadata") {
		t.Fatalf("expected WWW-Authenticate header on 401, got %q", got)
	}
	mode.Store(1)
	if got := ProbeWWWAuthenticate(ctx, srvURL+"/mcp"); got != "" {
		t.Fatalf("expected empty WWW-Authenticate on 200, got %q", got)
	}
	mode.Store(0)

	// RegisterClient branches.
	if _, err := RegisterClient(ctx, "javascript:alert(1)", "https://cb", ""); err == nil {
		t.Fatal("expected error for invalid regEndpoint")
	}
	for _, m := range []int32{5, 6, 7, 8, 9} {
		mode.Store(m)
		if _, err := RegisterClient(ctx, srvURL+"/register", "https://cb", ""); err == nil {
			t.Fatalf("expected RegisterClient error in mode %d", m)
		}
	}
	mode.Store(0)
	reg, err := RegisterClient(ctx, srvURL+"/register", "https://cb", "")
	if err != nil || reg.ClientID != "dcr-id-1" || reg.TokenEndpointAuthMethod != OAuthAuthMethodBasic {
		t.Fatalf("expected default auth method basic, got %+v err=%v", reg, err)
	}
}

func TestOAuthCoreHelpersTokenParsingAndHTTPClient(t *testing.T) {
	orig := oauthAllowLoopbackForTest
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = orig })

	t.Setenv("AGENTFLOW_SESSION_SECRET", "core-oauth-test-secret")
	t.Setenv("LOOPFORGE_OAUTH_TENCENT_DOCS_CLIENT_ID", "td-cid")
	t.Setenv("LOOPFORGE_OAUTH_TENCENT_DOCS_CLIENT_SECRET", "td-sec")
	t.Setenv("LOOPFORGE_OAUTH_TENCENT_DOCS_SCOPES", "all, doc:read")

	if _, ok := FindOAuthPreset("FEISHU"); !ok {
		t.Fatal("expected case-insensitive preset lookup")
	}
	if _, ok := FindOAuthPreset("unknown"); ok {
		t.Fatal("expected false for unknown preset")
	}
	app := EnvOAuthApp("tencent-docs")
	if app.ClientID != "td-cid" || app.ClientSecret != "td-sec" || len(app.Scopes) != 2 {
		t.Fatalf("unexpected EnvOAuthApp: %+v", app)
	}

	// State signing & verification error cases.
	key, err := oauthStateSecret()
	if err != nil {
		t.Fatal(err)
	}
	validState := signState(key, oauthStatePayload{Nonce: "n1", Exp: time.Now().Add(time.Minute).Unix()})
	if nonce, err := VerifyOAuthStateNonce(validState); err != nil || nonce != "n1" {
		t.Fatalf("VerifyOAuthStateNonce failed: nonce=%q err=%v", nonce, err)
	}
	if _, err := verifyStateAt(key, "bad", time.Now()); err == nil {
		t.Fatal("expected invalid format error")
	}
	if _, err := verifyStateAt(key, validState+"0", time.Now()); err == nil {
		t.Fatal("expected invalid signature error")
	}
	expiredState := signState(key, oauthStatePayload{Nonce: "n1", Exp: 100})
	if _, err := verifyStateAt(key, expiredState, time.Unix(200, 0)); err == nil {
		t.Fatal("expected expired state error")
	}

	// Token headers, CIMD, expiry parsing.
	h, p := oauthTokenHeader(domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthTokenHeader": "X-UAT", "oauthTokenPrefix": ""}})
	if h != "X-UAT" || p != "" {
		t.Fatalf("unexpected custom token header: %q %q", h, p)
	}
	t.Setenv("LOOPFORGE_OAUTH_CIMD_ENABLED", "")
	if cimdClientID("https://app.example.com") != "" {
		t.Fatal("CIMD should be disabled by default")
	}
	t.Setenv("LOOPFORGE_OAUTH_CIMD_ENABLED", "1")
	if cimdClientID("http://app.example.com") != "" {
		t.Fatal("CIMD must reject http://")
	}
	if got := cimdClientID("https://app.example.com/"); got != "https://app.example.com/oauth-client-metadata.json" {
		t.Fatalf("unexpected CIMD client_id: %q", got)
	}
	if parseSecretExpiry("") != nil || parseSecretExpiry("bad") != nil || parseSecretExpiry("2026-11-27T10:00:00.123456Z") == nil {
		t.Fatal("unexpected parseSecretExpiry behavior")
	}

	// parseTokenResponse error/variant shapes.
	if _, err := parseTokenResponse([]byte("{bad")); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	if _, err := parseTokenResponse([]byte(`{"code":1001,"msg":"bad"}`)); err == nil {
		t.Fatal("expected non-zero code error")
	}
	if _, err := parseTokenResponse([]byte(`{"error":"invalid_grant","error_description":"expired"}`)); err == nil {
		t.Fatal("expected OAuth error")
	}
	if _, err := parseTokenResponse([]byte(`{"ok":true}`)); err == nil {
		t.Fatal("expected missing access_token error")
	}
	parsed, err := parseTokenResponse([]byte(`{"access_token":"at-1","expires_in_seconds":"120"}`))
	if err != nil || parsed.ExpiresIn != 120 {
		t.Fatalf("expected string expires_in_seconds=120, got %+v err=%v", parsed, err)
	}

	// exchangeToken & redirect checks.
	if _, err := exchangeToken(context.Background(), tokenRequest{}); err == nil {
		t.Fatal("expected error for missing TokenURL")
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token-err":
			w.WriteHeader(http.StatusBadRequest)
			writeJSONBody(w, map[string]any{"error_description": "bad code"})
		case "/cross":
			http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()

	if _, err := exchangeToken(context.Background(), tokenRequest{TokenURL: srv.URL + "/token-err", Code: "c1"}); err == nil || !strings.Contains(err.Error(), "bad code") {
		t.Fatalf("expected HTTP 400 error description, got %v", err)
	}
	if _, _, _, err := oauthFetch(context.Background(), http.MethodGet, srv.URL+"/cross", "", nil, "", ""); err == nil || !strings.Contains(err.Error(), "跨源重定向") {
		t.Fatalf("expected cross-origin redirect rejection, got %v", err)
	}
	if _, _, _, err := oauthFetch(context.Background(), http.MethodGet, srv.URL+"/loop", "", nil, "", ""); err == nil || !strings.Contains(err.Error(), "重定向次数过多") {
		t.Fatalf("expected max redirects error, got %v", err)
	}
}

func TestServiceOAuthErrorPathsCIMDAndCAS(t *testing.T) {
	orig := oauthAllowLoopbackForTest
	oauthAllowLoopbackForTest = true
	t.Cleanup(func() { oauthAllowLoopbackForTest = orig })
	t.Setenv("AGENTFLOW_SESSION_SECRET", "svc-oauth-extra-secret")

	// pickTokenAuthMethod branches.
	for _, tc := range []struct {
		in   []string
		want string
		err  bool
	}{
		{nil, OAuthAuthMethodPost, false},
		{[]string{"client_secret_basic", "none"}, OAuthAuthMethodBasic, false},
		{[]string{"client_secret_post"}, OAuthAuthMethodPost, false},
		{[]string{"none"}, OAuthAuthMethodNone, false},
		{[]string{"private_key_jwt"}, "", true},
	} {
		got, err := pickTokenAuthMethod(tc.in)
		if (err != nil) != tc.err || got != tc.want {
			t.Fatalf("pickTokenAuthMethod(%v) = (%q, %v), want (%q, err=%v)", tc.in, got, err, tc.want, tc.err)
		}
	}

	var tokenMode atomic.Int32
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
			writeJSONBody(w, map[string]any{"authorization_servers": []string{srvURL}})
		case "/.well-known/oauth-authorization-server":
			writeJSONBody(w, map[string]any{
				"issuer":                              srvURL,
				"authorization_endpoint":              srvURL + "/authorize?prompt=consent",
				"token_endpoint":                      srvURL + "/token",
				"client_id_metadata_document_supported": true,
			})
		case "/token":
			switch tokenMode.Load() {
			case 1:
				writeJSONBody(w, map[string]any{"access_token": ""}) // empty access token
			default:
				writeJSONBody(w, map[string]any{"access_token": "at-new", "refresh_token": "rt-new", "expires_in": 3600})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	baseStore := newMemoryConnectorStore()
	casStore := &casFailStore{memoryConnectorStore: baseStore}
	svc := NewService(casStore, func() string { return "id1" })
	ctx := context.Background()

	// StartOAuth errors.
	if _, _, err := svc.StartOAuth(ctx, 1, "missing", "https://app.example.com"); !isAppErrKind(err, apperr.NotFound) {
		t.Fatalf("expected NotFound, got %v", err)
	}
	nonOAuth, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		Name:     "Plain",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srvURL + "/mcp",
		Secret:   domain.KnowledgeConnectorSecret{Token: "plain-tok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.StartOAuth(ctx, 1, nonOAuth.ID, "https://app.example.com"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for non-OAuth connector, got %v", err)
	}
	if sec, err := svc.EnsureValidSecret(ctx, 1, nonOAuth.ID, false, ""); err != nil || sec.AuthType != domain.ConnectorAuthBearer || sec.Token != "plain-tok" {
		t.Fatalf("EnsureValidSecret on non-OAuth failed: %+v err=%v", sec, err)
	}

	autoConn, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		ID:       "kc-auto",
		Name:     "Auto MCP",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srvURL + "/mcp",
		Config:   &domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthMode": OAuthModeAuto}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.StartOAuth(ctx, 1, autoConn.ID, ""); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when publicURL missing, got %v", err)
	}
	// Auto mode without DCR or CIMD fails with Invalid.
	t.Setenv("LOOPFORGE_OAUTH_CIMD_ENABLED", "")
	if _, _, err := svc.StartOAuth(ctx, 1, autoConn.ID, "https://app.example.com"); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when no client registration method available, got %v", err)
	}

	// Enable CIMD + test authorizeEndpoint with existing '?' uses '&'.
	t.Setenv("LOOPFORGE_OAUTH_CIMD_ENABLED", "1")
	authURL, _, err := svc.StartOAuth(ctx, 1, autoConn.ID, "https://app.example.com")
	if err != nil || !strings.Contains(authURL, "?prompt=consent&") || !strings.Contains(authURL, "oauth-client-metadata.json") {
		t.Fatalf("expected CIMD authorize URL with '&', got %q err=%v", authURL, err)
	}

	// CompleteOAuthCallback error paths.
	if _, err := svc.CompleteOAuthCallback(ctx, domain.ConnectorTypeMCP, "bad-state", "c", ""); err == nil {
		t.Fatal("expected state invalid error")
	}
	state := authorizeState(t, authURL)
	// Type mismatch -> OAuthCallbackCodeConnectorChanged.
	var cbErr *OAuthCallbackError
	if _, err := svc.CompleteOAuthCallback(ctx, domain.ConnectorTypeNotion, state, "c", ""); !errors.As(err, &cbErr) || cbErr.Code != OAuthCallbackCodeConnectorChanged {
		t.Fatalf("expected OAuthCallbackCodeConnectorChanged, got %v", err)
	}
	// Replay of already-consumed state -> OAuthCallbackCodeStateInvalid.
	if _, err := svc.CompleteOAuthCallback(ctx, domain.ConnectorTypeMCP, state, "c", ""); !errors.As(err, &cbErr) || cbErr.Code != OAuthCallbackCodeStateInvalid {
		t.Fatalf("expected OAuthCallbackCodeStateInvalid on replay, got %v", err)
	}

	// errParam & empty code callbacks.
	authURL2, _, err := svc.StartOAuth(ctx, 1, autoConn.ID, "https://app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteOAuthCallback(ctx, domain.ConnectorTypeMCP, authorizeState(t, authURL2), "", "access_denied"); !errors.As(err, &cbErr) || cbErr.Code != OAuthCallbackCodeFailed {
		t.Fatalf("expected OAuthCallbackCodeFailed on errParam, got %v", err)
	}
	authURL3, _, err := svc.StartOAuth(ctx, 1, autoConn.ID, "https://app.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteOAuthCallback(ctx, domain.ConnectorTypeMCP, authorizeState(t, authURL3), "", ""); !errors.As(err, &cbErr) || cbErr.Code != OAuthCallbackCodeFailed {
		t.Fatalf("expected OAuthCallbackCodeFailed on empty code, got %v", err)
	}

	// EnsureValidSecret error & CAS conflict paths.
	if _, err := svc.EnsureValidSecret(ctx, 1, "missing", false, ""); !isAppErrKind(err, apperr.NotFound) {
		t.Fatalf("expected NotFound, got %v", err)
	}
	// Unauthorized connector with no refresh token -> Forbidden.
	if _, err := svc.EnsureValidSecret(ctx, 1, autoConn.ID, false, ""); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden when RefreshToken empty, got %v", err)
	}

	// Seed expired OAuth connector with RefreshToken; test missing tokenURL -> Invalid, empty access_token -> Upstream, CAS conflict -> Conflict.
	customConn, err := svc.CreateConnector(ctx, 1, ConnectorInput{
		ID:       "kc-cust",
		Name:     "Custom OAuth",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srvURL + "/mcp",
		Config:   &domain.KnowledgeConnectorConfig{Extra: map[string]string{"oauthMode": OAuthModeCustom, "oauthTokenUrl": srvURL + "/token"}},
		Secret: domain.KnowledgeConnectorSecret{
			Token:         "at-old",
			RefreshToken:  "rt-old",
			ExpiresAt:     time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
			OAuthTokenURL: srvURL + "/token",
			OAuthClientID: "cid",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tokenMode.Store(1)
	if _, err := svc.EnsureValidSecret(ctx, 1, customConn.ID, false, ""); !isAppErrKind(err, apperr.Upstream) {
		t.Fatalf("expected Upstream when refresh returns empty access_token, got %v", err)
	}
	tokenMode.Store(0)

	casStore.failSecretCAS.Store(2)
	if _, err := svc.EnsureValidSecret(ctx, 1, customConn.ID, false, ""); !isAppErrKind(err, apperr.Conflict) {
		t.Fatalf("expected Conflict when CAS fails twice, got %v", err)
	}
	// Now CAS succeeds and updates token.
	sec, err := svc.EnsureValidSecret(ctx, 1, customConn.ID, false, "")
	if err != nil || sec.Token != "at-new" {
		t.Fatalf("expected refreshed token at-new, got %+v err=%v", sec, err)
	}
	// Non-expired token returns directly without refresh.
	if sec2, err := svc.EnsureValidSecret(ctx, 1, customConn.ID, false, ""); err != nil || sec2.Token != "at-new" {
		t.Fatalf("expected cached valid token, got %+v err=%v", sec2, err)
	}

	// OAuthPresets & decorateOAuth statuses.
	if presets := svc.OAuthPresets(); len(presets) != 4 {
		t.Fatalf("expected 4 builtin presets, got %+v", presets)
	}
	decorated, _ := svc.GetConnector(1, customConn.ID)
	if decorated.OAuthStatus != "authorized" {
		t.Fatalf("expected authorized status, got %q", decorated.OAuthStatus)
	}
	// Corrupted secret ciphertext -> unauthorized.
	rawSaved, _ := baseStore.GetKnowledgeConnector(1, customConn.ID)
	rawSaved.SecretEnc = "corrupted"
	if decBad := svc.decorateOAuth(rawSaved); decBad.OAuthStatus != "unauthorized" {
		t.Fatalf("expected unauthorized for corrupted secret, got %q", decBad.OAuthStatus)
	}

	// Pending eviction & clearOAuthState.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		svc.putPending("expired-nonce", pendingOAuth{UID: 1, ConnectorID: customConn.ID, ExpiresAt: time.Now().Add(-time.Second)})
	}()
	wg.Wait()
	svc.clearOAuthState(1, customConn.ID)
}

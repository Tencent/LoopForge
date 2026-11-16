package httpapi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/engine"
	"agentflow/internal/repo"

	"golang.org/x/oauth2"
)

const oidcTestOrigin = "https://loopforge.test"

func TestOIDCLogin(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	type grant struct {
		challenge string
		claims    map[string]any
	}
	var grants sync.Map
	var exchanges atomic.Int32
	var issuer string
	idp := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/keys":
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB",
			}}})
		case "/token":
			exchanges.Add(1)
			id, secret, ok := r.BasicAuth()
			raw, found := grants.Load(r.FormValue("code"))
			if !ok || id != "loopforge" || secret != "test-only" || !found ||
				r.FormValue("redirect_uri") != oidcTestOrigin+auth.OIDCCallbackPath ||
				r.FormValue("grant_type") != "authorization_code" ||
				oauth2.S256ChallengeFromVerifier(r.FormValue("code_verifier")) != raw.(grant).challenge {
				http.Error(w, "unexpected token request", http.StatusBadRequest)
				return
			}
			token, err := signOIDCTestToken(key, raw.(grant).claims)
			if err != nil {
				http.Error(w, "signing failed", http.StatusInternalServerError)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "test-access", "token_type": "Bearer", "id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	issuer = "http://" + idp.Listener.Addr().String()
	idp.Start()
	defer idp.Close()

	t.Setenv("AGENTFLOW_OIDC_ISSUER", issuer)
	t.Setenv("AGENTFLOW_OIDC_CLIENT_ID", "loopforge")
	t.Setenv("AGENTFLOW_OIDC_CLIENT_SECRET", "test-only")
	t.Setenv("AGENTFLOW_OIDC_REDIRECT_URL", oidcTestOrigin+auth.OIDCCallbackPath)
	t.Setenv("AGENTFLOW_OIDC_TOKEN_AUTH_METHOD", "")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SSO_LOGIN_URL", "")
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "")
	t.Setenv("AGENTFLOW_PREVIEW_LOCAL_LOGIN", "")
	t.Setenv("AGENTFLOW_SECURE_COOKIES", "")

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	user, err := store.CreateLocalAccount("existing-user", "password-for-test", false)
	if err != nil {
		t.Fatal(err)
	}
	subject := "Pilot-Subject"
	if err := store.DB.Exec("INSERT INTO oidc_identities (issuer, subject, user_id) VALUES (?, ?, ?)", issuer, subject, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	handler := New(store, engine.New(store, nil), nil, "", "")
	get := func(target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	returnPath := "/?projectId=platform#/issues"
	current := get(oidcTestOrigin + "/api/auth/current?return_url=" + url.QueryEscape(oidcTestOrigin+returnPath))
	var currentUser struct {
		LoginURL string    `json:"loginUrl"`
		User     auth.User `json:"user"`
	}
	if err := json.Unmarshal(current.Body.Bytes(), &currentUser); err != nil || currentUser.LoginURL != oidcLoginPage(returnPath, "") {
		t.Fatalf("guest did not receive deep link: %d %s", current.Code, current.Body.String())
	}
	setup := get(oidcTestOrigin + "/api/auth/setup?return_url=" + url.QueryEscape(returnPath))
	var loginPage struct {
		OIDCLoginURL string `json:"oidcLoginUrl"`
	}
	if err := json.Unmarshal(setup.Body.Bytes(), &loginPage); err != nil || loginPage.OIDCLoginURL == "" {
		t.Fatalf("login page did not receive OIDC entry: %d %s", setup.Code, setup.Body.String())
	}
	if rec := get("https://wrong-host.test" + loginPage.OIDCLoginURL); rec.Code != http.StatusNotFound {
		t.Fatalf("OIDC on a different host = %d", rec.Code)
	}

	start := func(t *testing.T, startURL, code string, overrides map[string]any) (string, *http.Cookie) {
		t.Helper()
		rec := get(oidcTestOrigin + startURL)
		location, err := url.Parse(rec.Header().Get("Location"))
		if err != nil || rec.Code != http.StatusFound || location.Path != "/authorize" {
			t.Fatalf("start redirect = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		q := location.Query()
		if q.Get("scope") != "openid" || q.Get("redirect_uri") != oidcTestOrigin+auth.OIDCCallbackPath ||
			q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
			t.Fatalf("authorization parameters = %v", q)
		}
		browser := oidcTestCookie(rec, "__Host-agentflow_oidc")
		if browser == nil || !browser.HttpOnly || !browser.Secure || browser.SameSite != http.SameSiteLaxMode || browser.Domain != "" {
			t.Fatalf("unexpected browser binding cookie: %v", browser)
		}
		claims := map[string]any{
			"iss": issuer, "sub": subject, "aud": "loopforge", "nonce": q.Get("nonce"),
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(),
		}
		for claim, value := range overrides {
			claims[claim] = value
		}
		grants.Store(code, grant{challenge: q.Get("code_challenge"), claims: claims})
		callback := url.Values{"state": {q.Get("state")}, "code": {code}, "return_url": {"https://other.test/"}}
		return oidcTestOrigin + auth.OIDCCallbackPath + "?" + callback.Encode(), browser
	}

	t.Run("existing user and one-time callback", func(t *testing.T) {
		callback, browser := start(t, loginPage.OIDCLoginURL, "valid", nil)
		if rec := get(callback); rec.Header().Get("Location") != oidcLoginPage("/", "invalid") {
			t.Fatalf("callback without original browser = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		results := make(chan *httptest.ResponseRecorder, 2)
		before := exchanges.Load()
		for range 2 {
			go func() { results <- get(callback, browser) }()
		}
		var session *http.Cookie
		for range 2 {
			rec := <-results
			switch rec.Header().Get("Location") {
			case returnPath:
				session = oidcTestCookie(rec, authCookieName)
			case oidcLoginPage("/", "invalid"):
				if oidcTestCookie(rec, authCookieName) != nil {
					t.Fatal("replayed callback set a session")
				}
			default:
				t.Fatalf("unexpected callback = %d %q", rec.Code, rec.Header().Get("Location"))
			}
		}
		if session == nil || !session.Secure || !session.HttpOnly || exchanges.Load()-before != 1 {
			t.Fatalf("expected one exchange and a secure session, got %d exchanges", exchanges.Load()-before)
		}
		rec := get(oidcTestOrigin+"/api/auth/current", session)
		if err := json.Unmarshal(rec.Body.Bytes(), &currentUser); err != nil || currentUser.User.ID != user.ID || currentUser.User.LoginName != user.LoginName || currentUser.User.IsGuest {
			t.Fatalf("session did not retain the original identity: %d %s", rec.Code, rec.Body.String())
		}
	})

	for _, tc := range []struct {
		name, reason string
		claims       map[string]any
	}{
		{"different subject case", "unlinked", map[string]any{"sub": "pilot-subject"}},
		{"different issuer", "invalid", map[string]any{"iss": issuer + "/another-tenant"}},
		{"different audience", "invalid", map[string]any{"aud": "another-app"}},
		{"additional audience", "invalid", map[string]any{"aud": []string{"loopforge", "another-app"}, "azp": "loopforge"}},
		{"different nonce", "invalid", map[string]any{"nonce": "another-flow"}},
		{"expired token", "invalid", map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callback, browser := start(t, auth.OIDCStartPath+"?return_url=%2F%252Fevil.test", tc.name, tc.claims)
			rec := get(callback, browser)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != oidcLoginPage("/", tc.reason) || oidcTestCookie(rec, authCookieName) != nil {
				t.Fatalf("unexpected rejected callback = %d %q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

func oidcTestCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func signOIDCTestToken(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign ID token: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

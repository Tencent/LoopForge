package repo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/codexoauth"
	"agentflow/internal/domain"
)

func testCodexJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func testCodexLogin(t *testing.T, access, refresh string, exp time.Time) string {
	t.Helper()
	raw, err := codexoauth.NewAuthFile(codexoauth.Tokens{
		IDToken:      testCodexJWT(t, map[string]any{"email": "dev@example.com"}),
		AccessToken:  testCodexJWT(t, map[string]any{"exp": exp.Unix(), "jti": access}),
		RefreshToken: refresh,
	}, time.Now()).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type fakeCodexRefresher struct {
	mu    sync.Mutex
	calls []string
	reply func(refreshToken string) (codexoauth.Tokens, error)
}

func (f *fakeCodexRefresher) Refresh(_ context.Context, refreshToken string) (codexoauth.Tokens, error) {
	f.mu.Lock()
	f.calls = append(f.calls, refreshToken)
	f.mu.Unlock()
	return f.reply(refreshToken)
}

func (f *fakeCodexRefresher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func useFakeCodexRefresher(t *testing.T, f *fakeCodexRefresher) {
	t.Helper()
	previous := codexOAuthClient
	codexOAuthClient = func() codexRefresher { return f }
	t.Cleanup(func() { codexOAuthClient = previous })
}

func codexLoginStore(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutUser(domain.User{LoginName: "codex-owner"}); err != nil {
		t.Fatal(err)
	}
	user, ok := st.UserByLoginName("codex-owner")
	if !ok {
		t.Fatal("user missing")
	}
	return st, user.ID
}

func sandboxTokens(t *testing.T, raw string) (access, refresh string) {
	t.Helper()
	auth, err := codexoauth.ParseAuthFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	return auth.AccessToken(), auth.RefreshToken()
}

func TestSaveCodexChatGPTLoginValidatesAndStaysPrivate(t *testing.T) {
	st, id := codexLoginStore(t)
	if _, err := st.SaveCodexChatGPTLogin(id, `{"tokens":{"access_token":"a"}}`); err == nil || !strings.Contains(err.Error(), "refresh_token") {
		t.Fatalf("login without refresh token: %v", err)
	}
	if _, err := st.SaveCodexChatGPTLogin(id, "not json"); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	raw := testCodexLogin(t, "a1", "r1", time.Now().Add(240*time.Hour))
	saved, err := st.SaveCodexChatGPTLogin(id, raw)
	if err != nil {
		t.Fatal(err)
	}
	public := domain.PublicUser(saved)
	if !public.CodexChatGPTLoginConfigured || public.CodexChatGPTLogin != "" {
		t.Fatalf("public user: %+v", public)
	}
	body, _ := json.Marshal(public)
	if strings.Contains(string(body), "r1") || strings.Contains(string(body), "access_token") || strings.Contains(string(body), "dev@example.com") {
		t.Fatalf("public user leaks the login: %s", body)
	}
	// A login refresh of the identity keeps the credential.
	if err := st.PutUser(domain.User{ID: id, LoginName: "codex-owner", ChineseName: "改名"}); err != nil {
		t.Fatal(err)
	}
	if user, _ := st.User(id); user.CodexChatGPTLogin == "" {
		t.Fatal("PutUser dropped the ChatGPT login")
	}
	// Editing other credentials must not overwrite the login.
	if _, err := st.UpdateUserCredentials(id, UserCredentialUpdate{CodexAPIKey: "sk-test"}); err != nil {
		t.Fatal(err)
	}
	if user, _ := st.User(id); user.CodexChatGPTLogin == "" || user.CodexAPIKey != "sk-test" {
		t.Fatalf("credential update: login=%t key=%q", user.CodexChatGPTLogin != "", user.CodexAPIKey)
	}
	cleared, err := st.ClearCodexChatGPTLogin(id)
	if err != nil || cleared.CodexChatGPTLogin != "" || cleared.CodexAPIKey != "sk-test" {
		t.Fatalf("clear: %v %+v", err, cleared)
	}
	if auth, err := st.CodexChatGPTAuth(context.Background(), id); auth != "" || err != nil {
		t.Fatalf("auth after clear = %q, %v", auth, err)
	}
}

func TestCodexChatGPTAuthGivesSandboxCopyWithoutRefresh(t *testing.T) {
	st, id := codexLoginStore(t)
	fake := &fakeCodexRefresher{reply: func(string) (codexoauth.Tokens, error) { return codexoauth.Tokens{}, errors.New("unexpected") }}
	useFakeCodexRefresher(t, fake)
	raw := testCodexLogin(t, "a1", "r1", time.Now().Add(240*time.Hour))
	if _, err := st.SaveCodexChatGPTLogin(id, raw); err != nil {
		t.Fatal(err)
	}
	copy, err := st.CodexChatGPTAuth(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	access, refresh := sandboxTokens(t, copy)
	if refresh != "" || !strings.Contains(copy, `"refresh_token": ""`) {
		t.Fatalf("sandbox copy keeps the refresh token: %s", copy)
	}
	if want, _ := sandboxTokens(t, raw); access != want {
		t.Fatal("sandbox copy changed the access token")
	}
	if fake.count() != 0 {
		t.Fatalf("fresh login refreshed %d times", fake.count())
	}
}

func TestCodexChatGPTAuthRefreshesOnceAcrossConcurrentDispatches(t *testing.T) {
	st, id := codexLoginStore(t)
	fresh := testCodexJWT(t, map[string]any{"exp": time.Now().Add(240 * time.Hour).Unix(), "jti": "a2"})
	fake := &fakeCodexRefresher{reply: func(token string) (codexoauth.Tokens, error) {
		if token != "r1" {
			return codexoauth.Tokens{}, &codexoauth.Error{Status: 400, Code: "refresh_token_reused"}
		}
		time.Sleep(50 * time.Millisecond)
		return codexoauth.Tokens{AccessToken: fresh, RefreshToken: "r2"}, nil
	}}
	useFakeCodexRefresher(t, fake)
	if _, err := st.SaveCodexChatGPTLogin(id, testCodexLogin(t, "a1", "r1", time.Now().Add(10*time.Minute))); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 5)
	copies := make([]string, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copies[i], errs[i] = st.CodexChatGPTAuth(context.Background(), id)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
		if access, refresh := sandboxTokens(t, copies[i]); access != fresh || refresh != "" {
			t.Fatalf("dispatch %d got access=%q refresh=%q", i, access, refresh)
		}
	}
	if fake.count() != 1 {
		t.Fatalf("refresh calls = %d, want 1", fake.count())
	}
	stored, _ := st.User(id)
	if access, refresh := sandboxTokens(t, stored.CodexChatGPTLogin); access != fresh || refresh != "r2" {
		t.Fatalf("stored login access=%q refresh=%q", access, refresh)
	}
}

func TestCodexChatGPTTerminalRefreshNeedsRelogin(t *testing.T) {
	st, id := codexLoginStore(t)
	var calls atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.FormValue("refresh_token") != "r1" {
			t.Errorf("unexpected request %s refresh=%q", r.URL.Path, r.FormValue("refresh_token"))
		}
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer issuer.Close()
	t.Setenv("AGENTFLOW_CODEX_AUTH_BASE_URL", issuer.URL)
	if _, err := st.SaveCodexChatGPTLogin(id, testCodexLogin(t, "a1", "r1", time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := st.CodexChatGPTAuth(context.Background(), id); !errors.Is(err, ErrCodexChatGPTRelogin) {
			t.Fatalf("err = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("terminal login refreshed %d times", calls.Load())
	}
	user, _ := st.User(id)
	if user.CodexChatGPTLoginStatus != domain.CodexChatGPTNeedsRelogin || domain.PublicUser(user).CodexChatGPTLoginStatus != domain.CodexChatGPTNeedsRelogin {
		t.Fatalf("status = %q", user.CodexChatGPTLoginStatus)
	}
	// A new sign-in clears the mark.
	if _, err := st.SaveCodexChatGPTLogin(id, testCodexLogin(t, "a3", "r3", time.Now().Add(240*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CodexChatGPTAuth(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func TestCodexChatGPTRefreshKeepsSignInThatLandsMeanwhile(t *testing.T) {
	st, id := codexLoginStore(t)
	relogin := testCodexLogin(t, "user", "r-user", time.Now().Add(240*time.Hour))
	fake := &fakeCodexRefresher{}
	fake.reply = func(string) (codexoauth.Tokens, error) {
		if _, err := st.SaveCodexChatGPTLogin(id, relogin); err != nil {
			return codexoauth.Tokens{}, err
		}
		return codexoauth.Tokens{AccessToken: testCodexJWT(t, map[string]any{"exp": time.Now().Add(240 * time.Hour).Unix(), "jti": "stale"}), RefreshToken: "r2"}, nil
	}
	useFakeCodexRefresher(t, fake)
	if _, err := st.SaveCodexChatGPTLogin(id, testCodexLogin(t, "a1", "r1", time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	copy, err := st.CodexChatGPTAuth(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := sandboxTokens(t, relogin)
	if access, _ := sandboxTokens(t, copy); access != want {
		t.Fatal("dispatch did not get the new sign-in")
	}
	stored, _ := st.User(id)
	if _, refresh := sandboxTokens(t, stored.CodexChatGPTLogin); refresh != "r-user" {
		t.Fatalf("stored refresh token = %q, want the user's new sign-in", refresh)
	}
}

func TestCodexChatGPTTransientRefreshFailure(t *testing.T) {
	st, id := codexLoginStore(t)
	fake := &fakeCodexRefresher{reply: func(string) (codexoauth.Tokens, error) {
		return codexoauth.Tokens{}, fmt.Errorf("%w: timeout", codexoauth.ErrTransport)
	}}
	useFakeCodexRefresher(t, fake)
	if _, err := st.SaveCodexChatGPTLogin(id, testCodexLogin(t, "a1", "r1", time.Now().Add(10*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if copy, err := st.CodexChatGPTAuth(context.Background(), id); err != nil || copy == "" {
		t.Fatalf("a still valid token must be used: %q %v", copy, err)
	}
	if _, err := st.SaveCodexChatGPTLogin(id, testCodexLogin(t, "a1", "r1", time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CodexChatGPTAuth(context.Background(), id); err == nil || errors.Is(err, ErrCodexChatGPTRelogin) {
		t.Fatalf("expired token with transient failure: %v", err)
	}
	if user, _ := st.User(id); user.CodexChatGPTLoginStatus != "" {
		t.Fatalf("transient failure marked the login %q", user.CodexChatGPTLoginStatus)
	}
}

func TestRefreshDueCodexChatGPTLoginsOnlyTouchesDueLogins(t *testing.T) {
	st, id := codexLoginStore(t)
	if err := st.PutUser(domain.User{LoginName: "codex-other"}); err != nil {
		t.Fatal(err)
	}
	other, _ := st.UserByLoginName("codex-other")
	fake := &fakeCodexRefresher{reply: func(string) (codexoauth.Tokens, error) {
		return codexoauth.Tokens{AccessToken: testCodexJWT(t, map[string]any{"exp": time.Now().Add(240 * time.Hour).Unix()}), RefreshToken: "r-new"}, nil
	}}
	useFakeCodexRefresher(t, fake)
	if _, err := st.SaveCodexChatGPTLogin(id, testCodexLogin(t, "a1", "r-due", time.Now().Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveCodexChatGPTLogin(other.ID, testCodexLogin(t, "a2", "r-fresh", time.Now().Add(200*time.Hour))); err != nil {
		t.Fatal(err)
	}
	st.refreshDueCodexChatGPTLogins(context.Background(), time.Now())
	if fake.count() != 1 || fake.calls[0] != "r-due" {
		t.Fatalf("refresh calls = %v", fake.calls)
	}
	if user, _ := st.User(id); !strings.Contains(user.CodexChatGPTLogin, "r-new") {
		t.Fatal("due login was not saved")
	}
}

func TestCodexChatGPTRefreshWaitsForAnotherInstanceLease(t *testing.T) {
	st, id := codexLoginStore(t)
	previousWait, previousPoll := codexChatGPTLeaseWait, codexChatGPTLeasePoll
	codexChatGPTLeaseWait, codexChatGPTLeasePoll = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { codexChatGPTLeaseWait, codexChatGPTLeasePoll = previousWait, previousPoll })
	fake := &fakeCodexRefresher{reply: func(string) (codexoauth.Tokens, error) { return codexoauth.Tokens{}, errors.New("unexpected") }}
	useFakeCodexRefresher(t, fake)
	raw := testCodexLogin(t, "a1", "r1", time.Now().Add(10*time.Minute))
	if _, err := st.SaveCodexChatGPTLogin(id, raw); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.User(id)
	if ok, err := st.acquireCodexChatGPTLease(id); !ok || err != nil {
		t.Fatalf("acquire: %t %v", ok, err)
	}
	if _, err := st.refreshCodexChatGPT(context.Background(), id, stored.CodexChatGPTLogin); !errors.Is(err, errCodexChatGPTBusy) {
		t.Fatalf("err = %v", err)
	}
	// The dispatch still runs on the valid access token.
	if copy, err := st.CodexChatGPTAuth(context.Background(), id); err != nil || copy == "" {
		t.Fatalf("dispatch during a foreign refresh: %q %v", copy, err)
	}
	if fake.count() != 0 {
		t.Fatalf("refreshed under a foreign lease %d times", fake.count())
	}
}

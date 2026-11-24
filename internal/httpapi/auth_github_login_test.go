package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGitHubLoginRegistersLinksAndSignsIn(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "test-session-secret")
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "boss")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), nil, "", "")

	account := githubLoginAccount{ID: 4242, Login: "octocat", Name: "The Octocat"}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("client_secret") != "secret" || r.Form.Get("code") != "good" || r.Form.Get("redirect_uri") != "http://localhost/api/auth/github/login/callback" {
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_test"})
		case "/user":
			if r.Header.Get("Authorization") != "Bearer gho_test" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(account)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(github.Close)
	oldAuthorize, oldToken, oldUser := githubLoginAuthorizeURL, githubLoginTokenURL, githubLoginUserURL
	githubLoginAuthorizeURL, githubLoginTokenURL, githubLoginUserURL = github.URL+"/authorize", github.URL+"/token", github.URL+"/user"
	t.Cleanup(func() {
		githubLoginAuthorizeURL, githubLoginTokenURL, githubLoginUserURL = oldAuthorize, oldToken, oldUser
	})

	get := func(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://localhost"+path, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	setupGitHub := func() bool {
		var v struct{ GithubLogin bool }
		_ = json.Unmarshal(get("/api/auth/setup").Body.Bytes(), &v)
		return v.GithubLogin
	}
	start := func() (state string, nonce *http.Cookie) {
		w := get("/api/auth/github/login/start")
		if w.Code != http.StatusFound {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		location, _ := url.Parse(w.Header().Get("Location"))
		if !strings.HasPrefix(location.String(), github.URL+"/authorize?") || location.Query().Get("client_id") != "client" || location.Query().Get("scope") != "read:user" {
			t.Fatalf("authorize redirect: %s", location)
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == githubLoginCookieName {
				nonce = c
			}
		}
		if nonce == nil || !nonce.HttpOnly {
			t.Fatal("start must set an HttpOnly nonce cookie")
		}
		return location.Query().Get("state"), nonce
	}
	callback := func(state, code string, cookies ...*http.Cookie) (string, *http.Cookie) {
		w := get("/api/auth/github/login/callback?state="+url.QueryEscape(state)+"&code="+code, cookies...)
		if w.Code != http.StatusFound {
			t.Fatalf("callback: %d %s", w.Code, w.Body.String())
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == authCookieName && c.Value != "" {
				return w.Header().Get("Location"), c
			}
		}
		return w.Header().Get("Location"), nil
	}

	if setupGitHub() {
		t.Fatal("GitHub sign-in must stay off without a client secret")
	}
	if w := get("/api/auth/github/login/start"); w.Header().Get("Location") != "/login.html?github_error=unavailable" {
		t.Fatalf("disabled start: %d %s", w.Code, w.Header().Get("Location"))
	}
	t.Setenv("AGENTFLOW_GITHUB_LOGIN_CLIENT_ID", "client")
	t.Setenv("AGENTFLOW_GITHUB_LOGIN_CLIENT_SECRET", "secret")
	if !setupGitHub() {
		t.Fatal("setup must report GitHub sign-in once configured")
	}

	state, nonce := start()
	if location, _ := callback(state, "good", nonce); location != "/login.html?github_error=setup" {
		t.Fatalf("before the administrator exists: %s", location)
	}
	if _, err := s.CreateLocalAccount("owner", "a-long-enough-password", true); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(domain.User{LoginName: "octocat"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: welcomeProjectID, Name: "Welcome"}); err != nil {
		t.Fatal(err)
	}

	state, nonce = start()
	if location, session := callback(state, "good"); location != "/login.html?github_error=expired" || session != nil {
		t.Fatalf("missing nonce cookie must be refused: %s", location)
	}
	if location, session := callback(state+"x", "good", nonce); location != "/login.html?github_error=expired" || session != nil {
		t.Fatalf("tampered state must be refused: %s", location)
	}
	if location, session := callback(state, "bad", nonce); location != "/login.html?github_error=failed" || session != nil {
		t.Fatalf("rejected code: %s", location)
	}
	location, session := callback(state, "good", nonce)
	if location != "/" || session == nil {
		t.Fatalf("sign-in: %s", location)
	}
	u, ok := s.UserByGitHubID(account.ID)
	if !ok || u.LoginName != "gh-octocat" || u.ChineseName != "The Octocat" {
		t.Fatalf("a taken login must not be taken over: %+v", u)
	}
	welcome, _ := s.Project(welcomeProjectID)
	if len(welcome.Members) != 1 || welcome.Members[0].UserID != u.ID {
		t.Fatalf("new GitHub user must join the welcome project: %+v", welcome.Members)
	}
	current := get("/api/auth/current", session)
	if !strings.Contains(current.Body.String(), `"loginName":"gh-octocat"`) {
		t.Fatalf("session must belong to the GitHub user: %s", current.Body.String())
	}

	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "false")
	account.Login = "renamed-cat"
	state, nonce = start()
	if location, session := callback(state, "good", nonce); location != "/" || session == nil {
		t.Fatalf("linked account must sign in with sign-up closed and after a rename: %s", location)
	}
	if again, _ := s.UserByGitHubID(account.ID); again.ID != u.ID {
		t.Fatalf("rename must keep the same user: %d != %d", again.ID, u.ID)
	}
	account = githubLoginAccount{ID: 7, Login: "boss"}
	state, nonce = start()
	if location, _ := callback(state, "good", nonce); location != "/login.html?github_error=signup_closed" {
		t.Fatalf("new GitHub user with sign-up closed: %s", location)
	}

	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "true")
	state, nonce = start()
	if location, session := callback(state, "good", nonce); location != "/" || session == nil {
		t.Fatalf("second GitHub user: %s", location)
	}
	if boss, _ := s.UserByGitHubID(7); boss.LoginName != "gh-boss" || s.IsPlatformAdmin(boss.LoginName) {
		t.Fatalf("a GitHub login must not claim a configured administrator name: %+v", boss)
	}
}

package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalSignupOptionsForExternalIdentity(t *testing.T) {
	for _, mode := range []struct {
		name string
		env  map[string]string
	}{
		{name: "gateway", env: map[string]string{"AGENTFLOW_TRUST_IDENTITY_HEADERS": "true"}},
		{name: "SSO", env: map[string]string{"AGENTFLOW_SSO_APP_TOKEN": "app-token", "AGENTFLOW_SSO_LOGIN_URL": "https://sso.example.test/login"}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "false")
			t.Setenv("AGENTFLOW_SSO_APP_TOKEN", "")
			t.Setenv("AGENTFLOW_SSO_LOGIN_URL", "")
			for key, value := range mode.env {
				t.Setenv(key, value)
			}
			t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "true")
			h := &authHandlers{deps: &deps{}}
			needsSetup, signup, err := h.localSignupOptions()
			if err != nil || needsSetup || signup {
				t.Fatalf("setup=%v signup=%v err=%v", needsSetup, signup, err)
			}
		})
	}
}

// Only the app token and the login URL together switch the login page to
// SSO. Either one alone still serves password sign-in, so a fresh install
// must keep its browser setup step.
func TestLocalSignupOptionsPartialSSOKeepsPasswordSetup(t *testing.T) {
	for _, key := range []string{"AGENTFLOW_SSO_APP_TOKEN", "AGENTFLOW_SSO_LOGIN_URL"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "false")
			t.Setenv("AGENTFLOW_SSO_APP_TOKEN", "")
			t.Setenv("AGENTFLOW_SSO_LOGIN_URL", "")
			t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "")
			t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "false")
			t.Setenv(key, "configured")
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			h := &authHandlers{deps: &deps{repo: s}}
			needsSetup, signup, err := h.localSignupOptions()
			if err != nil || !needsSetup || !signup {
				t.Fatalf("setup=%v signup=%v err=%v", needsSetup, signup, err)
			}
		})
	}
}

func TestRegisterRejectsConfiguredPlatformAdmin(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "false")
	t.Setenv("AGENTFLOW_SSO_APP_TOKEN", "")
	t.Setenv("AGENTFLOW_SSO_LOGIN_URL", "")
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "alice")
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "true")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), nil, "", "")
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "/api/auth/setup", "")
	var setup struct{ NeedsSetup, SignupEnabled bool }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &setup) != nil || setup.NeedsSetup || !setup.SignupEnabled {
		t.Fatalf("setup with a configured admin: %d %s", w.Code, w.Body.String())
	}
	w = call("POST", "/api/auth/register", `{"login":"alice","password":"a-long-enough-password"}`)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "账号命令") {
		t.Fatalf("configured admin register: %d %s", w.Code, w.Body.String())
	}
	w = call("POST", "/api/auth/register", `{"login":"bob","password":"a-long-enough-password"}`)
	if w.Code != 200 || s.IsPlatformAdmin("bob") {
		t.Fatalf("regular signup while an admin is configured: %d admin=%v %s", w.Code, s.IsPlatformAdmin("bob"), w.Body.String())
	}
}

func TestRegisterFirstAccountBecomesAdminAndSignupToggle(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	t.Setenv("AGENTFLOW_TRUST_IDENTITY_HEADERS", "false")
	t.Setenv("AGENTFLOW_SSO_APP_TOKEN", "")
	t.Setenv("AGENTFLOW_SSO_LOGIN_URL", "")
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "")
	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "true")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), nil, "", "")
	call := func(method, path, origin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	setup := func() (needs, signup bool) {
		w := call("GET", "/api/auth/setup", "", "")
		var v struct{ NeedsSetup, SignupEnabled bool }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("setup: %d %s", w.Code, w.Body.String())
		}
		return v.NeedsSetup, v.SignupEnabled
	}
	register := func(login string) *httptest.ResponseRecorder {
		return call("POST", "/api/auth/register", "http://localhost", `{"login":"`+login+`","password":"a-long-enough-password"}`)
	}

	if needs, _ := setup(); !needs {
		t.Fatal("empty deployment must ask for setup")
	}
	if w := call("POST", "/api/auth/register", "https://attacker.invalid", `{"login":"owner","password":"a-long-enough-password"}`); w.Code != 403 {
		t.Fatalf("cross origin register: %d", w.Code)
	}
	if w := call("POST", "/api/auth/register", "http://localhost", `{"login":"owner","password":"short"}`); w.Code != 400 {
		t.Fatalf("short password: %d", w.Code)
	}
	w := register("owner")
	if w.Code != 200 || len(w.Result().Cookies()) != 1 || !strings.Contains(w.Body.String(), `"admin":true`) {
		t.Fatalf("first register: %d %s", w.Code, w.Body.String())
	}
	if !s.IsPlatformAdmin("owner") {
		t.Fatal("first account must administer the platform")
	}
	if needs, signup := setup(); needs || !signup {
		t.Fatalf("after setup: needs=%v signup=%v", needs, signup)
	}
	if w := register("owner"); w.Code != 409 {
		t.Fatalf("duplicate login: %d", w.Code)
	}
	if err := s.PutProject(domain.Project{ID: welcomeProjectID, Name: "Welcome"}); err != nil {
		t.Fatal(err)
	}
	w = register("member")
	if w.Code != 200 || s.IsPlatformAdmin("member") {
		t.Fatalf("member register: %d admin=%v", w.Code, s.IsPlatformAdmin("member"))
	}
	var registered struct{ User struct{ ID int64 } }
	_ = json.Unmarshal(w.Body.Bytes(), &registered)
	welcome, _ := s.Project(welcomeProjectID)
	if len(welcome.Members) != 1 || welcome.Members[0].UserID != registered.User.ID || registered.User.ID == 0 {
		t.Fatalf("new account must join the welcome project: %+v user=%d", welcome.Members, registered.User.ID)
	}

	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "false")
	if _, signup := setup(); signup {
		t.Fatal("signup must report disabled")
	}
	if w := register("visitor"); w.Code != 403 {
		t.Fatalf("disabled signup: %d", w.Code)
	}

	photo := call("GET", "/api/auth/photo/100/member.png", "", "")
	if photo.Code != 200 || photo.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(photo.Body.String(), ">M</text>") {
		t.Fatalf("local avatar: %d %s", photo.Code, photo.Body.String())
	}
}

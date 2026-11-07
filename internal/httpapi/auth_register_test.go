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

func TestRegisterFirstAccountBecomesAdminAndSignupToggle(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
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

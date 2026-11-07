package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPasswordLoginOriginCredentialsAndSession(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutUser(domain.User{LoginName: "public-user"}); err != nil {
		t.Fatal(err)
	}
	u, _ := s.UserByLoginName("public-user")
	if err := s.SetLocalPassword(u.ID, "a-local-test-password"); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), nil, "", "")
	login := func(origin, password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/api/auth/password", bytes.NewBufferString(`{"login":"public-user","password":"`+password+`"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := login("https://attacker.invalid", "a-local-test-password"); w.Code != 403 {
		t.Fatalf("cross origin: %d", w.Code)
	}
	if w := login("http://localhost", "wrong-password"); w.Code != 401 {
		t.Fatalf("incorrect password: %d", w.Code)
	}
	w := login("http://localhost", "a-local-test-password")
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie: %+v", cookies)
	}
	request := httptest.NewRequest("GET", "/api/bootstrap", nil)
	request.AddCookie(cookies[0])
	reply := httptest.NewRecorder()
	h.ServeHTTP(reply, request)
	if reply.Code != 200 {
		t.Fatalf("session did not authorize bootstrap: %d", reply.Code)
	}
	if s.IsPlatformAdmin("public-user") {
		t.Fatal("sign in must not grant administration")
	}
}

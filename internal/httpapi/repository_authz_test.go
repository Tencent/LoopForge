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

func TestRepositoryWritesRequireProjectOwner(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := New(s, engine.New(s, runtime.NewManager(t.TempDir())), nil, "", "")
	session := func(login string) *http.Cookie {
		r := httptest.NewRequest("POST", "http://localhost/api/auth/register", bytes.NewBufferString(`{"login":"`+login+`","password":"secret1"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || len(w.Result().Cookies()) != 1 {
			t.Fatalf("register %s: %d %s", login, w.Code, w.Body.String())
		}
		return w.Result().Cookies()[0]
	}
	call := func(cookie *http.Cookie, method, path, body string) int {
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	session("admin")
	owner, other := session("owner"), session("other")
	ownerUser, _ := s.UserByLoginName("owner")
	if err := s.PutProject(domain.Project{ID: "p1", Name: "P1", OwnerID: ownerUser.ID}); err != nil {
		t.Fatal(err)
	}
	add := `{"id":"r1","projectId":"p1","repoUrl":"https://github.com/acme/app","defaultBranch":"main","localPath":"/etc"}`
	if code := call(other, "POST", "/api/repositories", add); code != 403 {
		t.Fatalf("non-owner add: %d", code)
	}
	if code := call(owner, "POST", "/api/repositories", add); code != 200 {
		t.Fatalf("owner add: %d", code)
	}
	if saved, _ := s.Repository("r1"); saved.LocalPath != "" {
		t.Fatal("only platform administrators may point a repository at a host directory")
	}
	if code := call(other, "PUT", "/api/repositories", add); code != 403 {
		t.Fatalf("non-owner update: %d", code)
	}
	if code := call(other, "DELETE", "/api/repositories?id=r1", ""); code != 403 {
		t.Fatalf("non-owner delete: %d", code)
	}
	if _, ok := s.Repository("r1"); !ok {
		t.Fatal("repository must survive rejected writes")
	}
	if code := call(owner, "DELETE", "/api/repositories?id=r1", ""); code != 200 {
		t.Fatalf("owner delete: %d", code)
	}
}

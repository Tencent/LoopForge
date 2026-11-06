package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func selfJoinFixture(t *testing.T) (*repo.Store, http.Handler, map[string]domain.User) {
	t.Helper()
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "self-join-test-secret")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	users := map[string]domain.User{}
	for _, login := range []string{"sj-owner", "sj-joiner"} {
		if err := s.PutUser(domain.User{LoginName: login}); err != nil {
			t.Fatal(err)
		}
		users[login], _ = s.UserByLoginName(login)
	}
	owner := users["sj-owner"]
	if _, err := s.CreateLocalAccount("sj-first", "a-long-enough-password", true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []domain.Project{
		{ID: "open-a", Name: "Open A", OwnerID: owner.ID, SelfJoin: true},
		{ID: "open-b", Name: "Open B", OwnerID: owner.ID, SelfJoin: true},
		{ID: "closed", Name: "Closed", OwnerID: owner.ID},
		{ID: "gone", Name: "Gone", OwnerID: owner.ID, SelfJoin: true},
	} {
		if err := s.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if err := s.SetProjectArchived("gone", &now, owner.ID); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	return s, h, users
}

func memberIDs(p domain.Project) map[int64]bool {
	out := map[int64]bool{}
	for _, m := range p.Members {
		out[m.UserID] = true
	}
	return out
}

func TestSignupListsAndJoinsSelfJoinProjects(t *testing.T) {
	s, h, _ := selfJoinFixture(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := call("GET", "/api/auth/setup", "")
	var setup struct {
		JoinableProjects []joinableProject `json:"joinableProjects"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &setup) != nil {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	if len(setup.JoinableProjects) != 2 || setup.JoinableProjects[0].ID != "open-a" || setup.JoinableProjects[1].ID != "open-b" {
		t.Fatalf("setup must list live self-join projects only: %+v", setup.JoinableProjects)
	}

	for _, ids := range []string{`["closed"]`, `["gone"]`, `["missing"]`, `["open-a","closed"]`} {
		if w := call("POST", "/api/auth/register", `{"login":"picky","password":"a-long-enough-password","projectIds":`+ids+`}`); w.Code != 400 {
			t.Fatalf("register with %s: %d %s", ids, w.Code, w.Body.String())
		}
	}
	if _, exists := s.UserByLoginName("picky"); exists {
		t.Fatal("a rejected sign-up must not create the account")
	}

	w = call("POST", "/api/auth/register", `{"login":"newbie","password":"a-long-enough-password","projectIds":["open-b"," open-b ",""]}`)
	if w.Code != 200 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	newbie, _ := s.UserByLoginName("newbie")
	b, _ := s.Project("open-b")
	a, _ := s.Project("open-a")
	if !memberIDs(b)[newbie.ID] || memberIDs(a)[newbie.ID] || len(b.Members) != 1 {
		t.Fatalf("new account must join exactly the picked project: a=%+v b=%+v", a.Members, b.Members)
	}

	t.Setenv("AGENTFLOW_ALLOW_SIGNUP", "false")
	w = call("GET", "/api/auth/setup", "")
	setup.JoinableProjects = nil
	if json.Unmarshal(w.Body.Bytes(), &setup) != nil || len(setup.JoinableProjects) != 0 {
		t.Fatalf("closed sign-up must not list projects: %s", w.Body.String())
	}
}

func TestJoinProjectRequiresSelfJoin(t *testing.T) {
	s, h, users := selfJoinFixture(t)
	owner, joiner := users["sj-owner"], users["sj-joiner"]

	joined := requestJSONAuthed[domain.Project](t, h, joiner, http.MethodPost, "/api/projects/open-a/join", nil, 200)
	if !memberIDs(joined)[joiner.ID] {
		t.Fatalf("join must add the caller: %+v", joined.Members)
	}
	again := requestJSONAuthed[domain.Project](t, h, joiner, http.MethodPost, "/api/projects/open-a/join", nil, 200)
	if len(again.Members) != 1 {
		t.Fatalf("joining twice must be a no-op: %+v", again.Members)
	}
	requestJSONAuthed[map[string]string](t, h, joiner, http.MethodPost, "/api/projects/closed/join", nil, 403)
	requestJSONAuthed[map[string]string](t, h, joiner, http.MethodPost, "/api/projects/gone/join", nil, 404)
	requestJSONAuthed[map[string]string](t, h, joiner, http.MethodPost, "/api/projects/missing/join", nil, 404)
	requestJSONAuthed[domain.Project](t, h, owner, http.MethodPost, "/api/projects/closed/join", nil, 200)
	if closed, _ := s.Project("closed"); len(closed.Members) != 0 {
		t.Fatalf("owner join must not add a member row: %+v", closed.Members)
	}

	anon := httptest.NewRequest(http.MethodPost, "/api/projects/open-b/join", nil)
	anon.Header.Set("Origin", "http://"+anon.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, anon)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous join: %d %s", rec.Code, rec.Body.String())
	}

	// Members cannot open a project they do not own; only owner or admin can.
	requestJSONAuthed[map[string]any](t, h, joiner, http.MethodPut, "/api/projects", domain.Project{ID: "closed", Name: "Closed", SelfJoin: true}, 403)
	saved := requestJSONAuthed[domain.Project](t, h, owner, http.MethodPut, "/api/projects", domain.Project{ID: "closed", Name: "Closed", SelfJoin: true}, 200)
	if !saved.SelfJoin {
		t.Fatal("owner must be able to open the project for self-service joining")
	}
	requestJSONAuthed[domain.Project](t, h, joiner, http.MethodPost, "/api/projects/closed/join", nil, 200)
}

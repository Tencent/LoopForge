package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// A role without a project is platform-wide: every project's LOOPs can use it.
// Like platform rules and skills, only administrators may change it; project
// writers must copy it into their project first.
func TestPlatformAgentWritesRequirePlatformAdmin(t *testing.T) {
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
		r.RemoteAddr = "192.0.2.10:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || len(w.Result().Cookies()) != 1 {
			t.Fatalf("register %s: %d %s", login, w.Code, w.Body.String())
		}
		return w.Result().Cookies()[0]
	}
	call := func(cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	admin := session("admin")
	owner := session("owner")
	ownerUser, _ := s.UserByLoginName("owner")
	if err := s.PutProject(domain.Project{ID: "p1", Name: "P1", OwnerID: ownerUser.ID}); err != nil {
		t.Fatal(err)
	}
	platform := domain.AgentDefinition{ID: "platform-role", Name: "Platform", ExecutorID: "codebuddy", Prompt: "platform prompt", Version: 3}
	if err := s.PutAgent(platform); err != nil {
		t.Fatal(err)
	}
	unchanged := func(step string) {
		t.Helper()
		got, ok := s.Agent("platform-role")
		if !ok || got.ProjectID != "" || got.Prompt != "platform prompt" || got.Name != "Platform" {
			t.Fatalf("%s changed the platform role: ok=%v %+v", step, ok, got)
		}
	}

	// A project writer may not take over the platform role through any write path.
	hijack := `{"id":"platform-role","projectId":"p1","name":"Mine","executorId":"codebuddy","prompt":"owned"}`
	if w := call(owner, "PUT", "/api/agents", hijack); w.Code != http.StatusForbidden {
		t.Fatalf("project writer PUT platform role: %d %s", w.Code, w.Body.String())
	}
	unchanged("PUT")
	if w := call(owner, "DELETE", "/api/agents/platform-role", ""); w.Code != http.StatusForbidden {
		t.Fatalf("project writer DELETE platform role: %d %s", w.Code, w.Body.String())
	}
	unchanged("DELETE")
	if w := call(owner, "POST", "/api/agents/platform-role/revisions/rev-x/restore", ""); w.Code != http.StatusForbidden {
		t.Fatalf("project writer restore platform role: %d %s", w.Code, w.Body.String())
	}
	unchanged("restore")
	if err := s.PutAgent(domain.AgentDefinition{ID: "own-role", Name: "Own", ProjectID: "p1", ExecutorID: "codebuddy"}); err != nil {
		t.Fatal(err)
	}
	if w := call(owner, "POST", "/api/agents/own-role/clone", `{"id":"platform-role","name":"Over","projectId":"p1"}`); w.Code != http.StatusForbidden {
		t.Fatalf("clone onto the platform role id: %d %s", w.Code, w.Body.String())
	}
	unchanged("clone onto")

	// Copying the platform role into the project still works.
	if w := call(owner, "POST", "/api/agents/platform-role/clone", `{"id":"platform-copy","name":"Copy","projectId":"p1"}`); w.Code != http.StatusCreated {
		t.Fatalf("clone platform role into project: %d %s", w.Code, w.Body.String())
	}
	if copy, ok := s.Agent("platform-copy"); !ok || copy.ProjectID != "p1" || copy.Prompt != "platform prompt" {
		t.Fatalf("copy: ok=%v %+v", ok, copy)
	}
	// The project's own roles keep project write access.
	if w := call(owner, "PUT", "/api/agents", `{"id":"own-role","projectId":"p1","name":"Own 2","executorId":"codebuddy"}`); w.Code != http.StatusOK {
		t.Fatalf("project writer PUT own role: %d %s", w.Code, w.Body.String())
	}

	// New project and role ids are path segments of their APIs: ids that a URL
	// path cannot carry would create records nobody can open or delete.
	for _, id := range []string{"wa bad/id?x", "a%2Fb", strings.Repeat("p", 65)} {
		body, _ := json.Marshal(map[string]any{"id": id, "key": "BAD", "name": "Bad"})
		if w := call(admin, "POST", "/api/projects", string(body)); w.Code != http.StatusBadRequest {
			t.Fatalf("project id %q: %d %s", id, w.Code, w.Body.String())
		}
		role, _ := json.Marshal(map[string]any{"id": id, "projectId": "p1", "name": "Bad", "executorId": "codebuddy"})
		if w := call(owner, "PUT", "/api/agents", string(role)); w.Code != http.StatusBadRequest {
			t.Fatalf("role id %q: %d %s", id, w.Code, w.Body.String())
		}
		clone, _ := json.Marshal(map[string]any{"id": id, "projectId": "p1", "name": "Bad"})
		if w := call(owner, "POST", "/api/agents/own-role/clone", string(clone)); w.Code != http.StatusBadRequest {
			t.Fatalf("clone id %q: %d %s", id, w.Code, w.Body.String())
		}
	}
	// A record saved before the check stays editable.
	if err := s.PutAgent(domain.AgentDefinition{ID: "legacy role", Name: "Legacy", ProjectID: "p1", ExecutorID: "codebuddy"}); err != nil {
		t.Fatal(err)
	}
	if w := call(owner, "PUT", "/api/agents", `{"id":"legacy role","projectId":"p1","name":"Legacy 2","executorId":"codebuddy"}`); w.Code != http.StatusOK {
		t.Fatalf("legacy role edit: %d %s", w.Code, w.Body.String())
	}

	// Administrators manage platform roles.
	if w := call(admin, "DELETE", "/api/agents/platform-role", ""); w.Code != http.StatusNoContent {
		t.Fatalf("admin DELETE platform role: %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.Agent("platform-role"); ok {
		t.Fatal("admin delete kept the platform role")
	}
}

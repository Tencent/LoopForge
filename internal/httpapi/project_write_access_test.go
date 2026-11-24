package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectContentWritesRequireMembership(t *testing.T) {
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
	request := func(cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	call := func(cookie *http.Cookie, method, path, body string) int {
		return request(cookie, method, path, body).Code
	}
	session("admin")
	owner, member, viewer, other := session("owner"), session("member"), session("viewer"), session("other")
	ownerUser, _ := s.UserByLoginName("owner")
	memberUser, _ := s.UserByLoginName("member")
	viewerUser, _ := s.UserByLoginName("viewer")
	if err := s.PutProject(domain.Project{ID: "p1", Name: "P1", OwnerID: ownerUser.ID, Members: []domain.ProjectMember{
		{UserID: memberUser.ID, Role: "member"}, {UserID: viewerUser.ID, Role: "viewer"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: "shared", Name: "Shared"}); err != nil {
		t.Fatal(err)
	}

	issue := fmt.Sprintf(`{"id":"i1","projectId":"p1","title":"Fix login","assigneeUserId":%d}`, ownerUser.ID)
	if code := call(other, "POST", "/api/issues", issue); code != 403 {
		t.Fatalf("non-member create issue: %d", code)
	}
	if code := call(viewer, "POST", "/api/issues", issue); code != 403 {
		t.Fatalf("viewer create issue: %d", code)
	}
	if code := call(member, "POST", "/api/issues", issue); code != 200 {
		t.Fatalf("member create issue: %d", code)
	}
	t.Run("clarification catalog uses the responsible account", func(t *testing.T) {
		const path = "/api/issues/i1/clarify-models"
		models := []domain.CLIModel{{ID: "owner-model"}}
		const key = "owner-test-key"
		if _, err := s.UpdateUserCredentials(ownerUser.ID, repo.UserCredentialUpdate{CodexAPIKey: key, CodexModels: &models}); err != nil {
			t.Fatal(err)
		}
		for _, cookie := range []*http.Cookie{other, viewer} {
			if code := call(cookie, "GET", path, ""); code != 403 {
				t.Fatalf("clarification catalog requires project write access: %d", code)
			}
		}
		w := request(member, "GET", path, "")
		if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte(key)) {
			t.Fatalf("invalid clarification catalog response: %d", w.Code)
		}
		var catalog assistantModelCatalog
		if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
			t.Fatal(err)
		}
		if len(catalog.Models) != 1 || catalog.Models[0].ID != "codex:owner-model" {
			t.Fatalf("want the project owner's model: %+v", catalog.Models)
		}
	})
	if code := call(other, "PUT", "/api/issues", `{"id":"i1","projectId":"p1","title":"Hijacked"}`); code != 403 {
		t.Fatalf("non-member edit issue: %d", code)
	}
	if code := call(other, "POST", "/api/issues/i1/close", `{}`); code != 403 {
		t.Fatalf("non-member close issue: %d", code)
	}
	if saved, _ := s.Issue("i1"); saved.Title != "Fix login" || saved.Status == "closed" {
		t.Fatalf("issue changed by a non-member: %+v", saved)
	}
	if code := call(other, "POST", "/api/issues", `{"projectId":"shared","title":"Shared idea"}`); code != 200 {
		t.Fatalf("ownerless projects stay shared: %d", code)
	}

	submission := `{"projectId":"p1","title":"Runbook","content":"restart the worker"}`
	if code := call(other, "POST", "/api/knowledge/submissions", submission); code != 403 {
		t.Fatalf("non-member submits knowledge to p1: %d", code)
	}
	if code := call(viewer, "POST", "/api/knowledge/submissions", submission); code != 403 {
		t.Fatalf("viewer submits knowledge to p1: %d", code)
	}
	if code := call(member, "POST", "/api/knowledge/submissions", submission); code == 403 {
		t.Fatal("members may submit knowledge to their project")
	}
	if code := call(other, "POST", "/api/knowledge/submissions", `{"title":"Note","content":"no project"}`); code == 403 {
		t.Fatal("project-less submissions are not project content")
	}

	agent := `{"id":"a1","projectId":"p1","name":"Dev","instructions":"build"}`
	if code := call(other, "PUT", "/api/agents", agent); code != 403 {
		t.Fatalf("non-member write agent: %d", code)
	}
	if code := call(owner, "PUT", "/api/agents", agent); code != 200 {
		t.Fatalf("owner write agent: %d", code)
	}
	if code := call(other, "DELETE", "/api/agents/a1", ""); code != 403 {
		t.Fatalf("non-member delete agent: %d", code)
	}
	if code := call(other, "POST", "/api/agents/a1/clone", `{"id":"a1","name":"Mine","projectId":"shared"}`); code != 403 {
		t.Fatalf("clone must not overwrite another project's agent: %d", code)
	}
	if saved, _ := s.Agent("a1"); saved.ProjectID != "p1" || saved.Name != "Dev" {
		t.Fatalf("agent changed by a non-member: %+v", saved)
	}

	if err := s.PutRun(domain.Run{ID: "r1", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "p1"}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	if code := call(other, "POST", "/api/runs/r1/cancel", `{}`); code != 403 {
		t.Fatalf("non-member cancel run: %d", code)
	}
	if code := call(other, "POST", "/api/runs/r1/workspace/terminal", `{"command":"id"}`); code != 403 {
		t.Fatalf("non-member terminal: %d", code)
	}
	if code := call(other, "DELETE", "/api/runs/r1", ""); code != 403 {
		t.Fatalf("non-member delete run: %d", code)
	}
	if code := call(member, "DELETE", "/api/runs/r1", ""); code != 200 {
		t.Fatalf("member delete run: %d", code)
	}

	rule := `{"id":"rule1","projectId":"p1","name":"Style","content":"gofmt"}`
	if code := call(other, "PUT", "/api/rules", rule); code != 403 {
		t.Fatalf("non-member write rule: %d", code)
	}
	if code := call(owner, "PUT", "/api/rules", rule); code != 200 {
		t.Fatalf("owner write rule: %d", code)
	}
	if code := call(other, "DELETE", "/api/rules?id=rule1", ""); code != 403 {
		t.Fatalf("non-member delete rule: %d", code)
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "sb-global", Name: "Base", Driver: domain.SandboxDriverDocker, Dockerfile: &domain.SandboxDockerfile{Content: "FROM alpine"}}); err != nil {
		t.Fatal(err)
	}
	if code := call(owner, "PUT", "/api/sandboxes", `{"id":"sb-global","name":"Base","driver":"docker","dockerfile":{"content":"FROM evil"}}`); code != 403 {
		t.Fatalf("non-admin edit platform sandbox: %d", code)
	}
	if code := call(other, "PUT", "/api/sync/config", `{}`); code != 403 {
		t.Fatalf("non-admin sync config: %d", code)
	}
	if code := call(other, "POST", "/api/mcp-servers/custom", `{"scope":"project","projectId":"p1","server":{"id":"m1","name":"M","transport":"http","url":"https://example.com/mcp"}}`); code != 403 {
		t.Fatalf("non-member install project MCP: %d", code)
	}
}

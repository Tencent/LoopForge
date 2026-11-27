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
	if err := s.PutProject(domain.Project{ID: "ownerless-team", Name: "Ownerless team", Members: []domain.ProjectMember{
		{UserID: memberUser.ID, Role: "member"},
	}}); err != nil {
		t.Fatal(err)
	}
	if code := call(other, "POST", "/api/issues", `{"projectId":"ownerless-team","title":"Outsider idea"}`); code != 403 {
		t.Fatalf("ownerless projects with members are limited to members: %d", code)
	}
	if code := call(member, "POST", "/api/issues", `{"projectId":"ownerless-team","title":"Member idea"}`); code != 200 {
		t.Fatalf("member writes to ownerless team project: %d", code)
	}

	members := fmt.Sprintf(`[{"userId":%d,"role":" Maintainer "},{"userId":%d}]`, memberUser.ID, viewerUser.ID)
	before, _ := s.Project("p1")
	if code := call(owner, "PUT", "/api/projects", `{"id":"p1","name":"P1","membersVersion":"`+before.MembersVersion+`","members":`+members+`}`); code != 200 {
		t.Fatalf("owner saves member roles: %d", code)
	}
	saved, _ := s.Project("p1")
	roles := map[int64]string{}
	for _, m := range saved.Members {
		roles[m.UserID] = m.Role
	}
	if roles[memberUser.ID] != "maintainer" || roles[viewerUser.ID] != "member" {
		t.Fatalf("member roles not normalized: %+v", saved.Members)
	}
	bad := fmt.Sprintf(`{"id":"p1","name":"P1","members":[{"userId":%d,"role":"admin"}]}`, memberUser.ID)
	if code := call(owner, "PUT", "/api/projects", bad); code != 400 {
		t.Fatalf("unknown member role accepted: %d", code)
	}
	restore := fmt.Sprintf(`{"id":"p1","name":"P1","membersVersion":%q,"members":[{"userId":%d,"role":"member"},{"userId":%d,"role":"viewer"}]}`, saved.MembersVersion, memberUser.ID, viewerUser.ID)
	if code := call(owner, "PUT", "/api/projects", restore); code != 200 {
		t.Fatalf("restore member roles: %d", code)
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
	if code := call(owner, "PUT", "/api/sandboxes", `{"id":"sb-global","name":"Base","driver":"docker","dockerfile":{"content":"FROM alpine"},"privileged":true}`); code != 403 {
		t.Fatalf("non-admin edit platform sandbox: %d", code)
	}
	const sandboxBody = `{"id":"sb-project","projectId":"p1","name":"Docker","driver":"docker","dockerfile":{"content":"FROM alpine"},"privileged":%t}`
	enable := fmt.Sprintf(sandboxBody, true)
	if code := call(viewer, "PUT", "/api/sandboxes", enable); code != 403 {
		t.Fatalf("viewer create privileged sandbox: %d", code)
	}
	if code := call(member, "PUT", "/api/sandboxes", enable); code != 200 {
		t.Fatalf("project member create privileged sandbox: %d", code)
	}
	if saved, ok := s.Sandbox("sb-project"); !ok || !saved.Privileged {
		t.Fatalf("privileged mode was not saved: %+v", saved)
	}
	disable := fmt.Sprintf(sandboxBody, false)
	if code := call(other, "PUT", "/api/sandboxes", disable); code != 403 {
		t.Fatalf("non-member edit project sandbox: %d", code)
	}
	if code := call(member, "PUT", "/api/sandboxes", disable); code != 200 {
		t.Fatalf("project member disable privileged mode: %d", code)
	}
	if saved, ok := s.Sandbox("sb-project"); !ok || saved.Privileged {
		t.Fatalf("disabling privileged mode was not saved: %+v", saved)
	}
	if code := call(other, "PUT", "/api/sync/config", `{}`); code != 403 {
		t.Fatalf("non-admin sync config: %d", code)
	}
	if code := call(other, "POST", "/api/mcp-servers/custom", `{"scope":"project","projectId":"p1","server":{"id":"m1","name":"M","transport":"http","url":"https://example.com/mcp"}}`); code != 403 {
		t.Fatalf("non-member install project MCP: %d", code)
	}
}

func TestProjectContentReadsRequireMembership(t *testing.T) {
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
	request := func(cookie *http.Cookie, method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, nil)
		r.Header.Set("Origin", "http://localhost")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	admin := session("admin")
	owner, viewer, other := session("owner"), session("viewer"), session("other")
	ownerUser, _ := s.UserByLoginName("owner")
	viewerUser, _ := s.UserByLoginName("viewer")

	if err := s.PutProject(domain.Project{
		ID:              "p1",
		Key:             "P1",
		Name:            "Private Project",
		Description:     "Internal work",
		OwnerID:         ownerUser.ID,
		WorkflowID:      "generic-backend-delivery",
		TAPDWorkspaceID: "998877",
		Members:         []domain.ProjectMember{{UserID: viewerUser.ID, Role: "viewer"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: "shared", Key: "SH", Name: "Shared Project"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRepository(domain.Repository{ID: "repo1", ProjectID: "p1", Name: "secret-repo", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "i1", ProjectID: "p1", Title: "Private issue"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "i2", ProjectID: "shared", Title: "Shared issue"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{ID: "r1", WorkflowID: "wf-priv", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "p1", IssueID: "i1"}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "t1", IssueID: "i1", RunID: "r1", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "wf-priv", Name: "Private SOP", ProjectID: "p1", Description: "Secret steps"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "wf-shared", Name: "Shared SOP", ProjectID: "p1", Shared: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{ID: "r-orphan", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: ownerUser.ID}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "t-orphan", RunID: "r-orphan", Status: "completed"}); err != nil {
		t.Fatal(err)
	}

	// Default /api/projects and /api/bootstrap hide unjoined projects from non-members.
	var otherProjects []domain.Project
	if w := request(other, "GET", "/api/projects"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &otherProjects) != nil {
		t.Fatalf("other /api/projects: %d %s", w.Code, w.Body.String())
	}
	for _, p := range otherProjects {
		if p.ID == "p1" {
			t.Fatalf("non-member should not see private project in default /api/projects: %+v", otherProjects)
		}
	}

	// /api/projects?scope=all returns a redacted summary for join-request discovery.
	var allProjects []domain.Project
	if w := request(other, "GET", "/api/projects?scope=all"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &allProjects) != nil {
		t.Fatalf("other /api/projects?scope=all: %d %s", w.Code, w.Body.String())
	}
	var p1Summary *domain.Project
	for i := range allProjects {
		if allProjects[i].ID == "p1" {
			p1Summary = &allProjects[i]
		}
	}
	if p1Summary == nil || !p1Summary.Restricted || p1Summary.Description != "" || len(p1Summary.Members) != 0 || p1Summary.TAPDWorkspaceID != "" || p1Summary.WorkflowID != "" {
		t.Fatalf("expected redacted summary for non-member in scope=all, got %+v", p1Summary)
	}
	if w := request(other, "GET", "/api/projects?scope=all"); bytes.Contains(w.Body.Bytes(), []byte("Internal work")) || bytes.Contains(w.Body.Bytes(), []byte("0001-01-01")) {
		t.Fatalf("scope=all leaked description or zero timestamp: %s", w.Body.String())
	}

	var boot struct {
		Projects     []domain.Project            `json:"projects"`
		Repositories []domain.Repository         `json:"repositories"`
		Workflows    []domain.WorkflowDefinition `json:"workflows"`
	}
	if w := request(other, "GET", "/api/bootstrap"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &boot) != nil {
		t.Fatalf("other /api/bootstrap: %d %s", w.Code, w.Body.String())
	}
	for _, p := range boot.Projects {
		if p.ID == "p1" {
			t.Fatalf("non-member should not see p1 in /api/bootstrap")
		}
	}
	for _, r := range boot.Repositories {
		if r.ID == "repo1" {
			t.Fatalf("non-member should not see repo1 in /api/bootstrap")
		}
	}
	for _, wf := range boot.Workflows {
		if wf.ID == "wf-priv" {
			t.Fatalf("non-member should not see private workflow in /api/bootstrap")
		}
	}

	// Direct GET on private project, repo, issue, run, task, workflow, analytics, and orphaned run/task is forbidden for non-members.
	for _, path := range []string{
		"/api/projects/p1",
		"/api/repositories/repo1",
		"/api/issues/i1",
		"/api/runs/r1",
		"/api/tasks/t1",
		"/api/workflows/wf-priv",
		"/api/workflows/wf-priv/revisions",
		"/api/workflow-metrics?projectId=p1",
		"/api/analytics?projectId=p1",
	} {
		if w := request(other, "GET", path); w.Code != 403 {
			t.Fatalf("non-member GET %s: want 403, got %d %s", path, w.Code, w.Body.String())
		}
		if w := request(viewer, "GET", path); w.Code != 200 {
			t.Fatalf("viewer GET %s: want 200, got %d %s", path, w.Code, w.Body.String())
		}
		if w := request(admin, "GET", path); w.Code != 200 {
			t.Fatalf("admin GET %s: want 200, got %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/workflows/wf-priv/agents", "/api/workflows/wf-priv/executor-requirements"} {
		if w := request(other, "GET", path); w.Code != 403 {
			t.Fatalf("non-member GET %s: want 403, got %d %s", path, w.Code, w.Body.String())
		}
	}
	// Shared workflows stay readable to non-members; orphaned runs/tasks stay hidden from non-owners.
	if w := request(other, "GET", "/api/workflows/wf-shared"); w.Code != 200 {
		t.Fatalf("non-member GET shared workflow: want 200, got %d", w.Code)
	}
	for _, path := range []string{"/api/runs/r-orphan", "/api/tasks/t-orphan"} {
		if w := request(other, "GET", path); w.Code != 403 {
			t.Fatalf("non-owner GET %s: want 403, got %d", path, w.Code)
		}
		if w := request(owner, "GET", path); w.Code != 200 {
			t.Fatalf("owner GET %s: want 200, got %d", path, w.Code)
		}
	}
	var otherWorkflows []domain.WorkflowDefinition
	if w := request(other, "GET", "/api/workflows"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &otherWorkflows) != nil {
		t.Fatalf("other /api/workflows: %d %s", w.Code, w.Body.String())
	}
	for _, wf := range otherWorkflows {
		if wf.ID == "wf-priv" {
			t.Fatalf("non-member should not see wf-priv in /api/workflows")
		}
	}
	var otherMetrics []domain.WorkflowMetrics
	if w := request(other, "GET", "/api/workflow-metrics"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &otherMetrics) != nil {
		t.Fatalf("other /api/workflow-metrics: %d %s", w.Code, w.Body.String())
	}
	for _, m := range otherMetrics {
		if m.WorkflowID == "wf-priv" {
			t.Fatalf("non-member should not see wf-priv in /api/workflow-metrics")
		}
	}
	var otherAnalytics domain.AnalyticsSnapshot
	if w := request(other, "GET", "/api/analytics"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &otherAnalytics) != nil {
		t.Fatalf("other /api/analytics: %d %s", w.Code, w.Body.String())
	}
	if otherAnalytics.Activity.Requirements > 1 {
		t.Fatalf("non-member /api/analytics should exclude private project requirements, got %d", otherAnalytics.Activity.Requirements)
	}

	// List endpoints filter out unjoined project data for non-members.
	var otherIssues []domain.Issue
	if w := request(other, "GET", "/api/issues"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &otherIssues) != nil {
		t.Fatalf("other /api/issues: %d %s", w.Code, w.Body.String())
	}
	for _, item := range otherIssues {
		if item.ProjectID == "p1" {
			t.Fatalf("non-member should not see p1 issues: %+v", otherIssues)
		}
	}
	var otherP1Issues []domain.Issue
	if w := request(other, "GET", "/api/issues?projectId=p1"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &otherP1Issues) != nil || len(otherP1Issues) != 0 {
		t.Fatalf("non-member /api/issues?projectId=p1: want empty, got %d %+v", w.Code, otherP1Issues)
	}
	var otherRuns []domain.Run
	if w := request(other, "GET", "/api/runs"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &otherRuns) != nil || len(otherRuns) != 0 {
		t.Fatalf("non-member /api/runs: want empty, got %d %+v", w.Code, otherRuns)
	}
	var otherTasks []domain.Task
	if w := request(other, "GET", "/api/tasks"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &otherTasks) != nil || len(otherTasks) != 0 {
		t.Fatalf("non-member /api/tasks: want empty, got %d %+v", w.Code, otherTasks)
	}
	var otherRepos []domain.Repository
	if w := request(other, "GET", "/api/repositories"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &otherRepos) != nil {
		t.Fatalf("other /api/repositories: %d %s", w.Code, w.Body.String())
	}
	for _, r := range otherRepos {
		if r.ProjectID == "p1" {
			t.Fatalf("non-member should not see p1 repos: %+v", otherRepos)
		}
	}

	// Owner, viewer and platform admin still see p1's issues.
	for name, cookie := range map[string]*http.Cookie{"owner": owner, "viewer": viewer, "admin": admin} {
		var list []domain.Issue
		if w := request(cookie, "GET", "/api/issues?projectId=p1"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 1 {
			t.Fatalf("%s /api/issues?projectId=p1: want 1, got %d %+v", name, w.Code, list)
		}
	}
}

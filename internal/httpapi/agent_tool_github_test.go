package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

const githubToolTestToken = "ghp_github_tool_secret"

func TestMainBotGitHubToolsOnlyForAssistantWithProject(t *testing.T) {
	for _, s := range []*AgentToolSession{
		nil,
		{Mode: AgentToolAssistant},
		{Mode: AgentToolAssistant, ProjectID: "  "},
		{Mode: AgentToolClarify, ProjectID: "p"},
		{Mode: AgentToolReport, ProjectID: "p"},
	} {
		if tools := mainBotGitHubTools(s); len(tools) != 0 {
			t.Fatalf("session %+v exposes github tools: %v", s, tools)
		}
	}
	tools := mainBotGitHubTools(&AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p"})
	want := map[string]bool{mainBotGitHubIssuesList: true, mainBotGitHubPullsList: true, mainBotGitHubPRStatus: true, mainBotGitHubCommentAdd: true, mainBotGitHubLabelsAdd: true}
	if len(tools) != len(want) {
		t.Fatalf("tools = %v", tools)
	}
	for _, tool := range tools {
		if !want[tool.Name] || tool.Kind != "github" {
			t.Fatalf("unexpected tool %+v", tool)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s has invalid schema: %v", tool.Name, err)
		}
	}
}

type githubToolFixture struct {
	gateway *agentToolGateway
	session *AgentToolSession
	user    domain.User
	set     func(token string)
}

func newGitHubToolFixture(t *testing.T) githubToolFixture {
	t.Helper()
	h, user := workbenchFixture(t)
	if err := h.repo.PutProject(domain.Project{ID: "other-project", Name: "Other", OwnerID: user.ID}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []domain.Repository{
		{ID: "gh-app", Name: "app", ProjectID: "workbench", CloneURL: "https://github.com/octo/app.git", DefaultBranch: "main"},
		{ID: "gl-internal", Name: "internal", ProjectID: "workbench", CloneURL: "https://gitlab.example.com/team/internal.git", DefaultBranch: "main"},
		{ID: "gh-foreign", Name: "foreign", ProjectID: "other-project", CloneURL: "https://github.com/octo/foreign.git", DefaultBranch: "main"},
	} {
		if err := h.repo.PutRepository(r); err != nil {
			t.Fatal(err)
		}
	}
	f := githubToolFixture{
		gateway: &agentToolGateway{deps: h.deps},
		session: &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: user.ID, User: user},
		user:    user,
	}
	f.set = func(token string) {
		t.Helper()
		updated, err := h.repo.UpdateUserCredentials(user.ID, repo.UserCredentialUpdate{GitToken: token})
		if err != nil {
			t.Fatal(err)
		}
		f.session.User = updated
	}
	return f
}

func TestResolveProjectGitHubRepoStaysInsideProject(t *testing.T) {
	f := newGitHubToolFixture(t)
	for _, name := range []string{"app", "APP", " app ", "gh-app"} {
		if path, err := f.gateway.resolveProjectGitHubRepo("workbench", name); err != nil || path != "octo/app" {
			t.Fatalf("%q: path=%q err=%v", name, path, err)
		}
	}
	for name, want := range map[string]string{
		"":            "repository is required",
		"internal":    "不是 GitHub 仓库",
		"foreign":     "找不到仓库",
		"gh-foreign":  "找不到仓库",
		"octo/app":    "找不到仓库",
		"octo/secret": "当前项目已绑定",
	} {
		path, err := f.gateway.resolveProjectGitHubRepo("workbench", name)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: path=%q err=%v, want %q", name, path, err, want)
		}
	}
	if _, err := f.gateway.resolveProjectGitHubRepo("empty-project", "app"); err == nil || !strings.Contains(err.Error(), "还没有绑定仓库") {
		t.Fatalf("empty project err = %v", err)
	}
}

type fakeGitHubAPI struct {
	mu       sync.Mutex
	requests []string
	tokens   []string
	bodies   []string
	routes   map[string]func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeGitHubAPI) serve(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		f.bodies = append(f.bodies, string(raw))
		route := f.routes[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if route == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		route(w, r)
	}))
	t.Cleanup(server.Close)
	previous := githubAPIBase
	githubAPIBase = server.URL
	t.Cleanup(func() { githubAPIBase = previous })
}

func githubJSON(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func callGitHubTool(t *testing.T, f githubToolFixture, name string, args string) (string, bool) {
	t.Helper()
	raw := json.RawMessage(`{"name":"` + name + `","arguments":` + args + `}`)
	result, err := f.gateway.callTool(context.Background(), f.session, raw)
	if err != nil {
		t.Fatalf("%s: callTool error %v", name, err)
	}
	content, _ := result["content"].([]map[string]any)
	if len(content) == 0 {
		t.Fatalf("%s: empty result %v", name, result)
	}
	text, _ := content[0]["text"].(string)
	isError, _ := result["isError"].(bool)
	return text, isError
}

func TestGitHubToolsRunWithoutWorkspaceInConfirmMode(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	if f.session.ExecMode.AutoWrite || f.session.RunID != "" {
		t.Fatalf("fixture must be a confirm-mode chat without a workspace: %+v", f.session)
	}
	var labelQuery string
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues": func(w http.ResponseWriter, r *http.Request) {
			labelQuery = r.URL.RawQuery
			_, _ = io.WriteString(w, `[
				{"number":1,"title":"Real issue","state":"open","html_url":"https://github.com/octo/app/issues/1","user":{"login":"alice"},"labels":[{"name":"bug"}]},
				{"number":2,"title":"A PR","state":"open","html_url":"https://github.com/octo/app/pull/2","user":{"login":"bob"},"pull_request":{"url":"x"}}
			]`)
		},
		"GET /repos/octo/app/pulls":                     githubJSON(http.StatusOK, `[{"number":7,"title":"Feature","state":"open","draft":true,"html_url":"https://github.com/octo/app/pull/7","user":{"login":"bob"},"head":{"ref":"topic"},"base":{"ref":"main"}}]`),
		"GET /repos/octo/app/pulls/7":                   githubJSON(http.StatusOK, `{"number":7,"title":"Feature","state":"open","merged":false,"mergeable_state":"blocked","html_url":"https://github.com/octo/app/pull/7","head":{"sha":"abc123","ref":"topic"},"base":{"ref":"main"}}`),
		"GET /repos/octo/app/commits/abc123/status":     githubJSON(http.StatusOK, `{"statuses":[{"context":"ci/legacy","state":"success","target_url":"https://ci.example.com/1"}]}`),
		"GET /repos/octo/app/commits/abc123/check-runs": githubJSON(http.StatusOK, `{"check_runs":[{"name":"unit","status":"in_progress","conclusion":null}]}`),
		"POST /repos/octo/app/issues/7/comments":        githubJSON(http.StatusCreated, `{"id":99,"html_url":"https://github.com/octo/app/pull/7#issuecomment-99"}`),
		"POST /repos/octo/app/issues/7/labels":          githubJSON(http.StatusOK, `[{"name":"bug"},{"name":"triage"}]`),
	}}
	api.serve(t)

	text, isErr := callGitHubTool(t, f, mainBotGitHubIssuesList, `{"repository":"app","labels":"bug"}`)
	if isErr || !strings.Contains(text, `"Real issue"`) || strings.Contains(text, `"A PR"`) || !strings.Contains(text, `"count":1`) {
		t.Fatalf("issues list = %s (error=%t)", text, isErr)
	}
	if !strings.Contains(labelQuery, "labels=bug") || !strings.Contains(labelQuery, "state=open") || !strings.Contains(labelQuery, "per_page=20") {
		t.Fatalf("issues query = %q", labelQuery)
	}

	text, isErr = callGitHubTool(t, f, mainBotGitHubPullsList, `{"repository":"app"}`)
	if isErr || !strings.Contains(text, `"head":"topic"`) || !strings.Contains(text, `"draft":true`) {
		t.Fatalf("pulls list = %s (error=%t)", text, isErr)
	}

	text, isErr = callGitHubTool(t, f, mainBotGitHubPRStatus, `{"repository":"app","number":7}`)
	if isErr || !strings.Contains(text, `"checksState":"pending"`) || !strings.Contains(text, `"mergeableState":"blocked"`) || !strings.Contains(text, `"ci/legacy"`) {
		t.Fatalf("pr status = %s (error=%t)", text, isErr)
	}

	text, isErr = callGitHubTool(t, f, mainBotGitHubCommentAdd, `{"repository":"app","number":7,"body":"LGTM"}`)
	if isErr || !strings.Contains(text, `"commentId":99`) {
		t.Fatalf("comment = %s (error=%t)", text, isErr)
	}

	text, isErr = callGitHubTool(t, f, mainBotGitHubLabelsAdd, `{"repository":"app","number":7,"labels":["bug"," triage "]}`)
	if isErr || !strings.Contains(text, `"labels":["bug","triage"]`) {
		t.Fatalf("labels = %s (error=%t)", text, isErr)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	for i, token := range api.tokens {
		if token != "Bearer "+githubToolTestToken {
			t.Fatalf("request %s used token %q", api.requests[i], token)
		}
	}
	bodies := strings.Join(api.bodies, "\n")
	if !strings.Contains(bodies, `{"body":"LGTM"}`) || !strings.Contains(bodies, `{"labels":["bug","triage"]}`) {
		t.Fatalf("write payloads = %s", bodies)
	}
}

func TestGitHubToolsRequirePersonalToken(t *testing.T) {
	f := newGitHubToolFixture(t)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubIssuesList, `{"repository":"app"}`)
	if !isErr || !strings.Contains(text, "个人凭证") || !strings.Contains(text, "github.com/settings/tokens") {
		t.Fatalf("missing token result = %s (error=%t)", text, isErr)
	}
	if len(api.requests) != 0 {
		t.Fatalf("GitHub called without a token: %v", api.requests)
	}
}

func TestGitHubToolsRejectForeignRepositoryWithoutCallingGitHub(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){}}
	api.serve(t)
	for _, args := range []string{
		`{"repository":"foreign","number":1,"body":"x"}`,
		`{"repository":"octo/foreign","number":1,"body":"x"}`,
		`{"repository":"internal","number":1,"body":"x"}`,
	} {
		if text, isErr := callGitHubTool(t, f, mainBotGitHubCommentAdd, args); !isErr {
			t.Fatalf("%s accepted: %s", args, text)
		}
	}
	if len(api.requests) != 0 {
		t.Fatalf("GitHub called for a repository outside the project: %v", api.requests)
	}
}

func TestGitHubToolsValidateArgumentsBeforeCallingGitHub(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){}}
	api.serve(t)
	for _, tc := range []struct {
		name, args, want string
	}{
		{mainBotGitHubIssuesList, `{"repository":"app","state":"merged"}`, "state"},
		{mainBotGitHubPRStatus, `{"repository":"app"}`, "number"},
		{mainBotGitHubCommentAdd, `{"repository":"app","number":1,"body":"   "}`, "body"},
		{mainBotGitHubCommentAdd, `{"repository":"app","number":0,"body":"x"}`, "number"},
		{mainBotGitHubLabelsAdd, `{"repository":"app","number":1,"labels":[]}`, "labels"},
		{mainBotGitHubLabelsAdd, `{"repository":"app","number":1,"labels":[" "]}`, "labels"},
	} {
		text, isErr := callGitHubTool(t, f, tc.name, tc.args)
		if !isErr || !strings.Contains(text, tc.want) {
			t.Fatalf("%s %s = %s (error=%t), want error mentioning %q", tc.name, tc.args, text, isErr, tc.want)
		}
	}
	if len(api.requests) != 0 {
		t.Fatalf("GitHub called with invalid arguments: %v", api.requests)
	}
}

func TestGitHubToolErrorsDoNotLeakToken(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"POST /repos/octo/app/issues/3/comments": githubJSON(http.StatusForbidden, `{"message":"token `+githubToolTestToken+` lacks Issues write"}`),
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubCommentAdd, `{"repository":"app","number":3,"body":"hi"}`)
	if !isErr || !strings.Contains(text, "HTTP 403") || strings.Contains(text, githubToolTestToken) {
		t.Fatalf("error result = %s (error=%t)", text, isErr)
	}
}

func TestGitHubCombinedChecksState(t *testing.T) {
	for _, tc := range []struct {
		name, status, runs, want string
	}{
		{"none", `{"statuses":[]}`, `{"check_runs":[]}`, "none"},
		{"all success", `{"statuses":[{"context":"a","state":"success"}]}`, `{"check_runs":[{"name":"b","status":"completed","conclusion":"success"}]}`, "success"},
		{"pending", `{"statuses":[{"context":"a","state":"pending"}]}`, `{"check_runs":[]}`, "pending"},
		{"failure wins", `{"statuses":[{"context":"a","state":"pending"}]}`, `{"check_runs":[{"name":"b","status":"completed","conclusion":"failure"}]}`, "failure"},
	} {
		api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
			"GET /repos/octo/app/commits/sha/status":     githubJSON(http.StatusOK, tc.status),
			"GET /repos/octo/app/commits/sha/check-runs": githubJSON(http.StatusOK, tc.runs),
		}}
		api.serve(t)
		if _, got := githubCombinedChecks(context.Background(), githubHTTPClient(), "tok", "octo/app", "sha"); got != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.name, got, tc.want)
		}
	}
	if _, got := githubCombinedChecks(context.Background(), githubHTTPClient(), "tok", "octo/app", " "); got != "none" {
		t.Fatalf("empty sha state = %q", got)
	}
}

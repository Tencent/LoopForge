package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	want := map[string]bool{mainBotGitHubIssuesList: true, mainBotGitHubIssueGet: true, mainBotGitHubPullsList: true, mainBotGitHubPRStatus: true, mainBotGitHubPRFiles: true, mainBotGitHubCommentAdd: true, mainBotGitHubLabelsAdd: true, mainBotGitHubPRReview: true, mainBotGitHubPRMerge: true, mainBotGitHubIssueClose: true, mainBotGitHubIssueCreate: true, mainBotGitHubPRClose: true}
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
		f.tokens = append(f.tokens, r.Header.Get("authorization"))
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
	if f.session.ExecMode.AutoWrite || f.session.ExecMode.ReviewWrites || f.session.RunID != "" {
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
	if isErr || !strings.Contains(text, `"Real issue"`) || strings.Contains(text, `"A PR"`) || !strings.Contains(text, `"count":1`) || !strings.Contains(text, `"hasMore":false`) {
		t.Fatalf("issues list = %s (error=%t)", text, isErr)
	}
	if !strings.Contains(labelQuery, "labels=bug") || !strings.Contains(labelQuery, "state=open") || !strings.Contains(labelQuery, "per_page=100") {
		t.Fatalf("issues query = %q", labelQuery)
	}

	text, isErr = callGitHubTool(t, f, mainBotGitHubPullsList, `{"repository":"app"}`)
	if isErr || !strings.Contains(text, `"head":"topic"`) || !strings.Contains(text, `"draft":true`) || !strings.Contains(text, `"hasMore":false`) {
		t.Fatalf("pulls list = %s (error=%t)", text, isErr)
	}

	text, isErr = callGitHubTool(t, f, mainBotGitHubPRStatus, `{"repository":"app","number":7}`)
	if isErr || !strings.Contains(text, `"checksState":"pending"`) || !strings.Contains(text, `"mergeableState":"blocked"`) || !strings.Contains(text, `"ci/legacy"`) || strings.Contains(text, "checksWarnings") {
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

func TestGitHubWriteToolsReviewedInAssistedMode(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	f.session.ExecMode = assistantExecModeForApproval(AssistantApprovalAssisted)
	if !f.session.ExecMode.ReviewWrites {
		t.Fatalf("assisted mode must review writes: %+v", f.session.ExecMode)
	}
	allow := false
	var reviewed []string
	f.session.approvalReviewer = func(_ context.Context, tool, description string, args map[string]any) assistantApprovalReview {
		reviewed = append(reviewed, tool)
		if args["repository"] != "octo/app" || args["projectId"] != "workbench" || args["arguments"] == nil || !strings.Contains(description, "GitHub") {
			t.Fatalf("review lost scope: %s %v", description, args)
		}
		outcome := "deny"
		if allow {
			outcome = "allow"
		}
		return assistantApprovalReview{Outcome: outcome, RiskLevel: "low", UserAuthorization: "high", Rationale: "核对本次 GitHub 写操作"}
	}
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues":             githubJSON(http.StatusOK, `[]`),
		"POST /repos/octo/app/issues/7/comments": githubJSON(http.StatusCreated, `{"id":99}`),
		"POST /repos/octo/app/issues/7/labels":   githubJSON(http.StatusOK, `[{"name":"bug"}]`),
	}}
	api.serve(t)

	// 只读工具不过审查。
	if text, isErr := callGitHubTool(t, f, mainBotGitHubIssuesList, `{"repository":"app"}`); isErr {
		t.Fatalf("read tool failed: %s", text)
	}
	// 参数不合法时先报错，不浪费一次审查。
	if _, isErr := callGitHubTool(t, f, mainBotGitHubCommentAdd, `{"repository":"app","number":7,"body":" "}`); !isErr {
		t.Fatal("invalid comment accepted")
	}
	if len(reviewed) != 0 {
		t.Fatalf("unexpected reviews: %v", reviewed)
	}

	for _, tc := range []struct{ name, args string }{
		{mainBotGitHubCommentAdd, `{"repository":"app","number":7,"body":"LGTM"}`},
		{mainBotGitHubLabelsAdd, `{"repository":"app","number":7,"labels":["bug"]}`},
	} {
		text, isErr := callGitHubTool(t, f, tc.name, tc.args)
		if !isErr || !strings.Contains(text, "帮我批准未执行") {
			t.Fatalf("%s denied review = %s (error=%t)", tc.name, text, isErr)
		}
	}
	api.mu.Lock()
	for _, req := range api.requests {
		if strings.HasPrefix(req, "POST ") {
			api.mu.Unlock()
			t.Fatalf("GitHub write executed without approval: %v", api.requests)
		}
	}
	api.mu.Unlock()
	if strings.Join(reviewed, ",") != mainBotGitHubCommentAdd+","+mainBotGitHubLabelsAdd {
		t.Fatalf("reviewed = %v", reviewed)
	}

	allow = true
	if text, isErr := callGitHubTool(t, f, mainBotGitHubCommentAdd, `{"repository":"app","number":7,"body":"LGTM"}`); isErr || !strings.Contains(text, `"commentId":99`) {
		t.Fatalf("approved comment = %s (error=%t)", text, isErr)
	}
	if text, isErr := callGitHubTool(t, f, mainBotGitHubLabelsAdd, `{"repository":"app","number":7,"labels":["bug"]}`); isErr || !strings.Contains(text, `"labels":["bug"]`) {
		t.Fatalf("approved labels = %s (error=%t)", text, isErr)
	}

	// 没有可用审查器时按拒绝处理，不静默放行。
	f.session.approvalReviewer = nil
	if text, isErr := callGitHubTool(t, f, mainBotGitHubCommentAdd, `{"repository":"app","number":7,"body":"again"}`); !isErr || !strings.Contains(text, "帮我批准未执行") {
		t.Fatalf("comment without reviewer = %s (error=%t)", text, isErr)
	}
}

func githubIssuePage(from, count int, pulls bool) string {
	items := make([]string, 0, count)
	for i := 0; i < count; i++ {
		n := from + i
		pr := ""
		if pulls {
			pr = `,"pull_request":{"url":"x"}`
		}
		items = append(items, fmt.Sprintf(`{"number":%d,"title":"item %d","state":"open"%s}`, n, n, pr))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestGitHubIssuesListPagesPastPullRequests(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	var queries []string
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		// 第 1 页全是 PR，第 2 页有 2 个 Issue 且不满页。
		"GET /repos/octo/app/issues": func(w http.ResponseWriter, r *http.Request) {
			queries = append(queries, r.URL.RawQuery)
			switch r.URL.Query().Get("page") {
			case "1":
				_, _ = io.WriteString(w, githubIssuePage(1000, githubListPageSize, true))
			case "2":
				_, _ = io.WriteString(w, githubIssuePage(1, 2, false))
			default:
				_, _ = io.WriteString(w, `[]`)
			}
		},
	}}
	api.serve(t)

	text, isErr := callGitHubTool(t, f, mainBotGitHubIssuesList, `{"repository":"app","limit":3}`)
	if isErr || !strings.Contains(text, `"count":2`) || !strings.Contains(text, `"hasMore":false`) || !strings.Contains(text, `"item 1"`) || !strings.Contains(text, `"item 2"`) {
		t.Fatalf("issues = %s (error=%t)", text, isErr)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "per_page=100") || !strings.Contains(queries[1], "page=2") {
		t.Fatalf("queries = %v", queries)
	}

	text, isErr = callGitHubTool(t, f, mainBotGitHubIssuesList, `{"repository":"app","limit":1}`)
	if isErr || !strings.Contains(text, `"count":1`) || !strings.Contains(text, `"hasMore":true`) || strings.Contains(text, `"item 2"`) {
		t.Fatalf("truncated issues = %s (error=%t)", text, isErr)
	}
}

func TestGitHubIssuesListStopsAtPageCap(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	pages := 0
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/issues": func(w http.ResponseWriter, r *http.Request) {
			pages++
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			_, _ = io.WriteString(w, githubIssuePage(page*1000, githubListPageSize, true))
		},
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubIssuesList, `{"repository":"app"}`)
	if isErr || !strings.Contains(text, `"count":0`) || !strings.Contains(text, `"hasMore":true`) || pages != githubIssuesMaxPages {
		t.Fatalf("issues = %s (error=%t) pages=%d", text, isErr, pages)
	}
}

func TestGitHubPullsListReportsHasMore(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	var query string
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/pulls": func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			_, _ = io.WriteString(w, `[{"number":9,"title":"first"},{"number":8,"title":"second"}]`)
		},
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubPullsList, `{"repository":"app","limit":1}`)
	if isErr || !strings.Contains(text, `"count":1`) || !strings.Contains(text, `"hasMore":true`) || strings.Contains(text, `"second"`) {
		t.Fatalf("pulls = %s (error=%t)", text, isErr)
	}
	if !strings.Contains(query, "per_page=2") {
		t.Fatalf("pulls query = %q", query)
	}
	text, _ = callGitHubTool(t, f, mainBotGitHubPullsList, `{"repository":"app","limit":2}`)
	if !strings.Contains(text, `"count":2`) || !strings.Contains(text, `"hasMore":false`) {
		t.Fatalf("pulls = %s", text)
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
	// 报错按操作说明所需权限，不再沿用"发起 PR"的文案。
	if !strings.Contains(text, "添加评论") || !strings.Contains(text, "Issues 读写") || strings.Contains(text, "发起 PR") {
		t.Fatalf("error text = %s", text)
	}
}

func TestGitHubCombinedChecksState(t *testing.T) {
	for _, tc := range []struct {
		name, status, runs, want string
	}{
		{"none", `{"statuses":[]}`, `{"check_runs":[]}`, "none"},
		{"all success", `{"statuses":[{"context":"a","state":"success"}]}`, `{"check_runs":[{"name":"b","status":"completed","conclusion":"success"}]}`, "success"},
		{"neutral and skipped pass", `{"statuses":[]}`, `{"check_runs":[{"name":"b","status":"completed","conclusion":"neutral"},{"name":"c","status":"completed","conclusion":"skipped"}]}`, "success"},
		{"pending", `{"statuses":[{"context":"a","state":"pending"}]}`, `{"check_runs":[]}`, "pending"},
		{"queued run", `{"statuses":[]}`, `{"check_runs":[{"name":"b","status":"queued","conclusion":null}]}`, "pending"},
		{"failure wins", `{"statuses":[{"context":"a","state":"pending"}]}`, `{"check_runs":[{"name":"b","status":"completed","conclusion":"failure"}]}`, "failure"},
		{"action_required", `{"statuses":[]}`, `{"check_runs":[{"name":"b","status":"completed","conclusion":"action_required"}]}`, "failure"},
		{"startup_failure", `{"statuses":[]}`, `{"check_runs":[{"name":"b","status":"completed","conclusion":"startup_failure"}]}`, "failure"},
		{"stale", `{"statuses":[]}`, `{"check_runs":[{"name":"b","status":"completed","conclusion":"stale"}]}`, "failure"},
	} {
		api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
			"GET /repos/octo/app/commits/sha/status":     githubJSON(http.StatusOK, tc.status),
			"GET /repos/octo/app/commits/sha/check-runs": githubJSON(http.StatusOK, tc.runs),
		}}
		api.serve(t)
		if _, got, warnings := githubCombinedChecks(context.Background(), githubHTTPClient(), "tok", "octo/app", "sha"); got != tc.want || len(warnings) != 0 {
			t.Errorf("%s: state = %q warnings = %v, want %q", tc.name, got, warnings, tc.want)
		}
	}
	if _, got, _ := githubCombinedChecks(context.Background(), githubHTTPClient(), "tok", "octo/app", " "); got != "none" {
		t.Fatalf("empty sha state = %q", got)
	}
}

func TestGitHubCombinedChecksReportsUnreadableSources(t *testing.T) {
	var runsQuery, statusQuery string
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/commits/sha/status": func(w http.ResponseWriter, r *http.Request) {
			statusQuery = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"total_count":1,"statuses":[{"context":"a","state":"success"}]}`)
		},
		"GET /repos/octo/app/commits/sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
			runsQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"Resource not accessible by personal access token"}`)
		},
	}}
	api.serve(t)
	checks, state, warnings := githubCombinedChecks(context.Background(), githubHTTPClient(), "tok", "octo/app", "sha")
	if state != "unknown" || len(checks) != 1 || len(warnings) != 1 || !strings.Contains(warnings[0], "HTTP 403") || !strings.Contains(warnings[0], "Checks") {
		t.Fatalf("403 on check-runs: state=%q checks=%v warnings=%v", state, checks, warnings)
	}
	if !strings.Contains(runsQuery, "per_page=100") || !strings.Contains(statusQuery, "per_page=100") {
		t.Fatalf("queries: status=%q runs=%q", statusQuery, runsQuery)
	}

	// 读到的部分已经失败时，另一个来源读不到（这里 status 返回 404）也不影响
	// failure 的结论；只统计了前 N 项时要提示。
	api = &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/commits/sha/check-runs": githubJSON(http.StatusOK, `{"total_count":150,"check_runs":[{"name":"b","status":"completed","conclusion":"failure"}]}`),
	}}
	api.serve(t)
	_, state, warnings = githubCombinedChecks(context.Background(), githubHTTPClient(), "tok", "octo/app", "sha")
	if state != "failure" || len(warnings) != 2 || !strings.Contains(strings.Join(warnings, "\n"), "共 150 项") {
		t.Fatalf("partial failure: state=%q warnings=%v", state, warnings)
	}
}

func TestGitHubPRStatusSurfacesCheckWarnings(t *testing.T) {
	f := newGitHubToolFixture(t)
	f.set(githubToolTestToken)
	api := &fakeGitHubAPI{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /repos/octo/app/pulls/7":                githubJSON(http.StatusOK, `{"number":7,"state":"open","head":{"sha":"abc","ref":"topic"},"base":{"ref":"main"}}`),
		"GET /repos/octo/app/commits/abc/status":     githubJSON(http.StatusForbidden, `{"message":"no"}`),
		"GET /repos/octo/app/commits/abc/check-runs": githubJSON(http.StatusForbidden, `{"message":"no"}`),
	}}
	api.serve(t)
	text, isErr := callGitHubTool(t, f, mainBotGitHubPRStatus, `{"repository":"app","number":7}`)
	if isErr || !strings.Contains(text, `"checksState":"unknown"`) || !strings.Contains(text, "checksWarnings") || !strings.Contains(text, "Commit statuses") {
		t.Fatalf("pr status = %s (error=%t)", text, isErr)
	}
}

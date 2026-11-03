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
)

func TestGitHubRepoPathOnlyAcceptsGitHubRepositories(t *testing.T) {
	for _, tc := range []struct {
		name string
		rc   domain.RepositoryContext
		want string
	}{
		{"project id", domain.RepositoryContext{GongfengProjectID: " github:octo/hello-world "}, "octo/hello-world"},
		{"https clone", domain.RepositoryContext{CloneURL: "https://github.com/octo/app.git"}, "octo/app"},
		{"ssh clone", domain.RepositoryContext{CloneURL: "git@github.com:octo/app.git"}, "octo/app"},
		{"web url fallback", domain.RepositoryContext{CloneURL: "https://gitlab.com/a/b.git", WebURL: "https://GitHub.com/octo/web/"}, "octo/web"},
		{"other host", domain.RepositoryContext{CloneURL: "https://gitlab.com/octo/app.git"}, ""},
		{"lookalike host", domain.RepositoryContext{CloneURL: "https://github.com.evil.test/octo/app.git"}, ""},
		{"nested path", domain.RepositoryContext{CloneURL: "https://github.com/octo/app/extra"}, ""},
		{"traversal", domain.RepositoryContext{GongfengProjectID: "github:../x"}, ""},
		{"bad characters", domain.RepositoryContext{GongfengProjectID: "github:octo/app?x=1"}, ""},
		{"empty", domain.RepositoryContext{}, ""},
	} {
		if got := githubRepoPath(tc.rc); got != tc.want {
			t.Errorf("%s: githubRepoPath = %q, want %q", tc.name, got, tc.want)
		}
	}
}

type fakeGitHubPulls struct {
	mu        sync.Mutex
	requests  []string
	tokens    []string
	bodies    []map[string]string
	create    func(w http.ResponseWriter)
	list      func(w http.ResponseWriter)
	update    func(w http.ResponseWriter)
	listQuery string
}

func (f *fakeGitHubPulls) serve(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		body := map[string]string{}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/app/pulls":
			f.create(w)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/app/pulls":
			f.listQuery = r.URL.RawQuery
			f.list(w)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/octo/app/pulls/7":
			f.update(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	previous := githubAPIBase
	githubAPIBase = server.URL
	t.Cleanup(func() { githubAPIBase = previous })
}

func githubRespond(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func TestCreateGitHubPROpensPullRequest(t *testing.T) {
	fake := &fakeGitHubPulls{create: githubRespond(http.StatusCreated, `{"number":42}`)}
	fake.serve(t)
	pr, err := createGitHubPR(context.Background(), "ghp_test", "octo/app", "lf/topic", "main", "Add feature", "## Summary")
	if err != nil || pr.IID != 42 || pr.Existing {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	if len(fake.requests) != 1 || fake.tokens[0] != "Bearer ghp_test" {
		t.Fatalf("requests=%v tokens=%v", fake.requests, fake.tokens)
	}
	if got := fake.bodies[0]; got["head"] != "lf/topic" || got["base"] != "main" || got["title"] != "Add feature" || got["body"] != "## Summary" {
		t.Fatalf("payload=%v", got)
	}
}

func TestCreateGitHubPRUpdatesExistingOpenPullRequest(t *testing.T) {
	fake := &fakeGitHubPulls{
		create: githubRespond(http.StatusUnprocessableEntity, `{"message":"Validation Failed","errors":[{"message":"A pull request already exists"}]}`),
		list:   githubRespond(http.StatusOK, `[{"number":7}]`),
		update: githubRespond(http.StatusOK, `{"number":7}`),
	}
	fake.serve(t)
	pr, err := createGitHubPR(context.Background(), "ghp_test", "octo/app", "lf/topic", "main", "New title", "New body")
	if err != nil || pr.IID != 7 || !pr.Existing || !pr.Updated || pr.UpdateError != "" {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	if !strings.Contains(fake.listQuery, "head=octo%3Alf%2Ftopic") || !strings.Contains(fake.listQuery, "state=open") || !strings.Contains(fake.listQuery, "base=main") {
		t.Fatalf("list query = %q", fake.listQuery)
	}
	if got := fake.bodies[2]; got["title"] != "New title" || got["body"] != "New body" {
		t.Fatalf("update payload = %v", got)
	}
}

func TestCreateGitHubPRReportsUpdateFailureWithoutFailingSubmission(t *testing.T) {
	fake := &fakeGitHubPulls{
		create: githubRespond(http.StatusUnprocessableEntity, `{"message":"exists"}`),
		list:   githubRespond(http.StatusOK, `[{"number":7}]`),
		update: githubRespond(http.StatusForbidden, `{"message":"Resource not accessible"}`),
	}
	fake.serve(t)
	pr, err := createGitHubPR(context.Background(), "ghp_test", "octo/app", "lf/topic", "main", "t", "d")
	if err != nil || pr.IID != 7 || pr.Updated || !strings.Contains(pr.UpdateError, "HTTP 403") || !strings.Contains(pr.UpdateError, "Resource not accessible") {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
}

func TestCreateGitHubPRExplainsFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   []string
	}{
		{http.StatusUnauthorized, `{"message":"Bad credentials"}`, []string{"HTTP 401", "Git Token 无效或已过期"}},
		{http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`, []string{"HTTP 403", "Pull requests", "Resource not accessible"}},
		{http.StatusNotFound, `{"message":"Not Found"}`, []string{"HTTP 404", "Not Found"}},
		{http.StatusUnprocessableEntity, `{"message":"Validation Failed","errors":[{"message":"No commits between main and lf/topic"}]}`, []string{"HTTP 422", "Validation Failed No commits"}},
		{http.StatusBadGateway, `upstream exploded`, []string{"HTTP 502", "upstream exploded"}},
	} {
		fake := &fakeGitHubPulls{create: githubRespond(tc.status, tc.body), list: githubRespond(http.StatusOK, `[]`)}
		fake.serve(t)
		_, err := createGitHubPR(context.Background(), "ghp_test", "octo/app", "lf/topic", "main", "t", "d")
		if err == nil {
			t.Fatalf("HTTP %d: expected an error", tc.status)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("HTTP %d: %q missing %q", tc.status, err, want)
			}
		}
		if strings.Contains(err.Error(), "ghp_test") {
			t.Errorf("HTTP %d: error leaks the token: %v", tc.status, err)
		}
	}
}

func TestCreateGitHubPRRejectsUnparseableCreateResponse(t *testing.T) {
	fake := &fakeGitHubPulls{create: githubRespond(http.StatusCreated, `{"number":0}`)}
	fake.serve(t)
	if _, err := createGitHubPR(context.Background(), "ghp_test", "octo/app", "a", "main", "t", "d"); err == nil || !strings.Contains(err.Error(), "无法解析") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateGitHubPRReportsUnreachableAPI(t *testing.T) {
	previous := githubAPIBase
	githubAPIBase = "http://127.0.0.1:1"
	t.Cleanup(func() { githubAPIBase = previous })
	if _, err := createGitHubPR(context.Background(), "ghp_test", "octo/app", "a", "main", "t", "d"); err == nil || !strings.Contains(err.Error(), "无法连接 GitHub") {
		t.Fatalf("err = %v", err)
	}
}

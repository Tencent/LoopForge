package httpapi

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
)

func TestGitLabPersonalRepositories(t *testing.T) {
	const token = "fixture-gitlab-pat"
	const base = "https://gitlab.example.test"
	var projectRequest bool
	transport := githubTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "gitlab.example.test" || req.Header.Get("PRIVATE-TOKEN") != token {
			t.Fatalf("unexpected GitLab request: %s", req.URL)
		}
		response := httptest.NewRecorder()
		switch req.URL.Path {
		case "/api/v4/personal_access_tokens/self":
			fmt.Fprint(response, `{"active":true,"scopes":["api"]}`)
		case "/api/v4/projects":
			if req.URL.Query().Get("membership") != "true" || req.URL.Query().Get("search") != "service" {
				t.Fatalf("unexpected query: %s", req.URL.RawQuery)
			}
			response.Header().Set("X-Next-Page", "2")
			fmt.Fprintf(response, "[%s,%s]", gitLabTestProject(base, 31), gitLabTestProject(base, 32))
		case "/api/v4/projects/31", "/api/v4/projects/32", "/api/v4/projects/34":
			projectRequest = true
			id, _ := strconv.Atoi(filepath.Base(req.URL.Path))
			fmt.Fprint(response, gitLabTestProject(base, id))
		case "/api/v4/projects/31/repository/branches", "/api/v4/projects/32/repository/branches":
			fmt.Fprint(response, `[]`)
		case "/api/v4/projects/33", "/api/v4/projects/34/repository/branches":
			response.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected GitLab path: %s", req.URL.Path)
		}
		return response.Result(), nil
	})
	gitLabTransport = transport
	t.Cleanup(func() { gitLabTransport = nil })
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "sample", Key: "SAMPLE", Name: "Sample", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	path := "/api/gitlab/credential"
	response := cvBRequest(t, f, http.MethodPut, path, map[string]string{"token": token, "url": base}, true)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"gitLabTokenConfigured":true`) || strings.Contains(response.Body.String(), token) {
		t.Fatalf("credential response = %d %s", response.Code, response.Body)
	}
	status := cvBRequest(t, f, http.MethodGet, "/api/bootstrap", nil, true)
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"gitLabURL":"`+base+`"`) || strings.Contains(status.Body.String(), token) {
		t.Fatalf("personal settings response = %d %s", status.Code, status.Body)
	}
	response = cvBRequest(t, f, http.MethodGet, "/api/gitlab/repositories?q=service", nil, true)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"hasMore":true`) || !strings.Contains(response.Body.String(), "sample/team/service-32") {
		t.Fatalf("list response = %d %s", response.Code, response.Body)
	}
	for _, id := range []int{31, 32, 33, 34} {
		body := map[string]any{"projectId": "sample", "provider": "gitlab", "gitLabProjectId": id, "cloneUrl": "https://elsewhere.example.test/unsafe.git"}
		response = cvBRequest(t, f, http.MethodPost, "/api/repositories", body, true)
		if id >= 33 {
			if response.Code != 403 {
				t.Fatalf("inaccessible binding response = %d %s", response.Code, response.Body)
			}
			continue
		}
		var repository domain.Repository
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &repository) != nil || repository.CloneURL != fmt.Sprintf("%s/sample/team/service-%d.git", base, id) || repository.DefaultBranch != "trunk" || repository.GitLabURL != base {
			t.Fatalf("bind response = %d %s", response.Code, response.Body)
		}
	}
	githubRepository := requestJSONAuthed[domain.Repository](t, f.h, f.user, http.MethodPost, "/api/repositories", map[string]string{
		"projectId": "sample", "repoUrl": "https://github.com/example/shared",
	}, http.StatusOK)
	repositories := requestJSONAuthed[[]domain.Repository](t, f.h, f.user, http.MethodGet, "/api/repositories?projectId=sample", nil, http.StatusOK)
	var githubFound bool
	gitLabCount := 0
	for _, repository := range repositories {
		if repository.ProjectID != "sample" {
			continue
		}
		if repository.Provider == domain.RepositoryProviderGitLab {
			gitLabCount++
		}
		if repository.Provider == "github" && repository.ID == githubRepository.ID {
			githubFound = true
		}
	}
	if !githubFound || gitLabCount != 2 {
		t.Fatalf("expected GitHub and two GitLab repositories on the same project, GitHub found: %t, GitLab count: %d", githubFound, gitLabCount)
	}
	if response := cvBRequest(t, f, http.MethodPut, fmt.Sprintf("/api/users/%d/credentials", f.user.ID), map[string]string{"gitToken": "test-github-credential"}, true); response.Code != 200 ||
		f.s.GitLabCredential(f.user.ID).Token != token {
		t.Fatal("saving GitHub credentials changed the GitLab PAT")
	}
	if !projectRequest {
		t.Fatal("binding did not validate the selected repository with GitLab")
	}
	if response := cvBRequest(t, f, http.MethodGet, "/api/gitlab/repositories", nil, false); response.Code != 401 {
		t.Fatalf("anonymous search = %d", response.Code)
	}
	response = cvBRequest(t, f, http.MethodDelete, path, nil, true)
	if response.Code != 200 || strings.Contains(response.Body.String(), `"gitLabTokenConfigured":true`) || f.s.GitCredential(t.Context(), f.user.ID) != "test-github-credential" {
		t.Fatalf("clear response = %d %s", response.Code, response.Body)
	}
}

func TestGitLabInstancesAreIsolatedByUser(t *testing.T) {
	const firstURL = "https://gitlab-one.example.test"
	const secondURL = "https://gitlab-two.example.test/code"
	gitLabTransport = githubTestTransport(func(req *http.Request) (*http.Response, error) {
		base, token, prefix := firstURL, "first-pat", "/api/v4/"
		if req.URL.Host == "gitlab-two.example.test" {
			base, token, prefix = secondURL, "second-pat", "/code/api/v4/"
		}
		if req.Header.Get("PRIVATE-TOKEN") != token || !strings.HasPrefix(req.URL.Path, prefix) {
			t.Fatalf("unexpected credential or endpoint for %s", req.URL)
		}
		response := httptest.NewRecorder()
		switch strings.TrimPrefix(req.URL.Path, prefix) {
		case "personal_access_tokens/self":
			fmt.Fprint(response, `{"active":true,"scopes":["api"]}`)
		case "projects":
			fmt.Fprintf(response, "[%s]", gitLabTestProject(base, 31))
		case "projects/31":
			fmt.Fprint(response, gitLabTestProject(base, 31))
		case "projects/31/repository/branches":
			fmt.Fprint(response, `[]`)
		default:
			t.Fatalf("unexpected GitLab endpoint %s", req.URL)
		}
		return response.Result(), nil
	})
	t.Cleanup(func() { gitLabTransport = nil })
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutUser(domain.User{LoginName: "second-gitlab-user"}); err != nil {
		t.Fatal(err)
	}
	second, _ := f.s.UserByLoginName("second-gitlab-user")
	const projectID = "multi-instance"
	if err := f.s.PutProject(domain.Project{ID: projectID, Key: "MI", Name: "Multiple instances", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range []struct {
		user        domain.User
		base, token string
	}{
		{f.user, firstURL, "first-pat"},
		{second, secondURL, "second-pat"},
	} {
		user, base := entry.user, entry.base
		settings := requestJSONAuthed[map[string]any](t, f.h, user, http.MethodPut, "/api/gitlab/credential", map[string]string{
			"url": base + "/", "token": entry.token,
		}, http.StatusOK)
		if settings["gitLabUrl"] != base {
			t.Fatalf("unexpected instance for user %d: %v", user.ID, settings["gitLabUrl"])
		}
		bootstrap := requestJSONAuthed[struct {
			URL   string           `json:"gitLabURL"`
			Users []map[string]any `json:"users"`
		}](t, f.h, user, http.MethodGet, "/api/bootstrap", nil, http.StatusOK)
		if bootstrap.URL != base {
			t.Fatalf("bootstrap returned another user's GitLab URL: %q", bootstrap.URL)
		}
		for _, user := range bootstrap.Users {
			if _, found := user["gitLabUrl"]; found {
				t.Fatal("GitLab instance leaked into the shared user directory")
			}
		}
		search := requestJSONAuthed[map[string]any](t, f.h, user, http.MethodGet, "/api/gitlab/repositories", nil, http.StatusOK)
		if search["gitLabUrl"] != base {
			t.Fatalf("repository search returned another user's GitLab URL: %v", search["gitLabUrl"])
		}
		body := map[string]any{"projectId": projectID, "provider": "gitlab", "gitLabProjectId": 31, "gitLabUrl": base}
		bound := requestJSONAuthed[domain.Repository](t, f.h, user, http.MethodPost, "/api/repositories", body, http.StatusOK)
		if bound.GitLabURL != base || !strings.HasPrefix(bound.CloneURL, base+"/") {
			t.Fatalf("repository bound to wrong instance: %+v", bound)
		}
		if len(ids) > 0 {
			body["id"] = ids[0]
			requestJSONAuthed[map[string]any](t, f.h, user, http.MethodPut, "/api/repositories", body, http.StatusBadRequest)
		}
		ids = append(ids, bound.ID)
	}
	if ids[0] == ids[1] {
		t.Fatal("identical GitLab project IDs collided across instances")
	}
}

func TestGitLabRunnerClaimUsesCurrentActorPAT(t *testing.T) {
	const base = "https://gitlab.example.test"
	const token = "test-rotated-runner-pat"
	f := cvBNewHTTPFixture(t)
	runnerID, session := registerTestRunner(t, f.h)
	task := domain.RunnerTask{ID: "gitlab-task", RunnerID: runnerID, Provider: "mock", Status: domain.RunnerTaskQueued, MaxAttempts: 1,
		Invocation: &domain.RunnerInvocation{GitLabUserID: f.user.ID, Repositories: []domain.RunnerRepository{{UseGitLab: true, CloneURL: base + "/team/project.git"}}}}
	if err := f.s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SaveGitLabToken(f.user.ID, base, token); err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + session}
	heartbeat := func(version string, supportsGitLabPAT bool) {
		t.Helper()
		body := map[string]any{"version": version}
		if supportsGitLabPAT {
			body["supportsGitLabPAT"] = true
		}
		response := requestWithHeaders(t, f.h, http.MethodPost, "/api/runners/heartbeat", body, headers)
		if response.Code != http.StatusOK {
			t.Fatalf("heartbeat returned HTTP %d", response.Code)
		}
		runner, ok := f.s.Runner(runnerID)
		if !ok || runner.RunnerVersion != version || runner.SupportsGitLabPAT != supportsGitLabPAT {
			t.Fatalf("heartbeat did not record version %s and GitLab PAT support %v", version, supportsGitLabPAT)
		}
	}
	claim := func(preflight, supportsGitLabPAT bool) *httptest.ResponseRecorder {
		t.Helper()
		body := map[string]any{
			"availableRuntimes": []string{"mock"}, "noWait": true, "preflight": preflight,
		}
		if supportsGitLabPAT {
			body["supportsGitLabPAT"] = true
		}
		return requestWithHeaders(t, f.h, http.MethodPost, "/api/runners/tasks/claim", body, headers)
	}
	for _, tc := range []struct {
		version         string
		staleCapability bool
	}{
		{"0.9.3", true}, // A downgraded process can claim before its first heartbeat.
		{"0.9.6", false},
	} {
		heartbeat(tc.version, tc.staleCapability)
		for _, preflight := range []bool{true, false} {
			if response := claim(preflight, false); response.Code != http.StatusNoContent {
				t.Fatalf("runner without GitLab PAT support got HTTP %d (preflight=%v)", response.Code, preflight)
			}
		}
	}
	heartbeat("0.9.3", true)
	if response := claim(true, true); response.Code != http.StatusOK {
		t.Fatalf("compatible runner preflight returned HTTP %d", response.Code)
	}
	response := claim(false, true)
	var claimed struct{ Task domain.RunnerTask }
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &claimed) != nil ||
		claimed.Task.Invocation == nil || claimed.Task.Invocation.GitLabToken != token || claimed.Task.Invocation.GitLabUserID != 0 {
		t.Fatalf("claim did not resolve the execution user's credential (HTTP %d)", response.Code)
	}
	stored, _ := f.s.RunnerTask(task.ID)
	if stored.Invocation == nil || stored.Invocation.GitLabToken != "" {
		t.Fatal("claimed PAT was persisted in the task")
	}
	response = requestWithHeaders(t, f.h, http.MethodGet, "/api/runners/tasks/"+task.ID, nil, headers)
	if response.Code != 200 || strings.Contains(response.Body.String(), token) || strings.Contains(response.Body.String(), "gitLabUserId") {
		t.Fatalf("runner status exposed private credential metadata (HTTP %d)", response.Code)
	}
}

func gitLabTestProject(base string, id int) string {
	path := fmt.Sprintf("sample/team/service-%d", id)
	return fmt.Sprintf(`{"id":%d,"name":"Service %d","path_with_namespace":%q,"http_url_to_repo":%q,"web_url":%q,"default_branch":"trunk"}`, id, id, path, base+"/"+path+".git", base+"/"+path)
}

// The real Git transport uses the responsible user's PAT, and the SOP sees
// exactly the repositories frozen at dispatch, even if more are bound.
func TestGitLabSOPChecksOutOnlySelectedRepository(t *testing.T) {
	const ownerPAT = "test-owner-pat"
	const developerPAT = "test-developer-pat"
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	namespace := filepath.Join(root, "team")
	if err := os.MkdirAll(namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	sshTestGit(t, source, "init", "-b", "trunk")
	sshTestGit(t, source, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "Initial")
	selected := filepath.Join(namespace, "frontend.git")
	sshTestGit(t, root, "clone", "--bare", source, selected)
	sshTestGit(t, selected, "config", "http.receivepack", "true")
	var readRequests, otherRequests atomic.Int32
	gitBackend := &cgi.Handler{
		Path: gitPath, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/team/frontend.git/") {
			otherRequests.Add(1)
			http.NotFound(w, r)
			return
		}
		if _, password, ok := r.BasicAuth(); !ok || password != developerPAT {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitLab fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		readRequests.Add(1)
		gitBackend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	ca := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", ca)
	t.Setenv("AGENTFLOW_GITLAB_PRIVATE_HOSTS", "127.0.0.1")
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutUser(domain.User{LoginName: "gitlab-developer"}); err != nil {
		t.Fatal(err)
	}
	developer, _ := f.s.UserByLoginName("gitlab-developer")
	for userID, pat := range map[int64]string{f.user.ID: ownerPAT, developer.ID: developerPAT} {
		if err := f.s.SaveGitLabToken(userID, server.URL, pat); err != nil {
			t.Fatal(err)
		}
	}
	project := domain.Project{ID: "gitlab-sop", Name: "GitLab SOP", OwnerID: f.user.ID, DefaultRepositoryID: "backend"}
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"frontend", "backend"} {
		if err := f.s.PutRepository(domain.Repository{
			ID: name, ProjectID: project.ID, Name: name, Provider: domain.RepositoryProviderGitLab,
			CloneURL: server.URL + "/team/" + name + ".git", DefaultBranch: "trunk",
		}); err != nil {
			t.Fatal(err)
		}
	}
	wf := domain.WorkflowDefinition{
		ID: "gitlab-sop-test", Name: "GitLab SOP test", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "verify", Name: "Verify checkout", Type: "script", ScriptLanguage: "shell",
			Script: `test -d frontend/.git && test ! -e backend && git -C frontend push origin HEAD:refs/heads/feature/gitlab-sop`}},
	}
	if err := f.s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "gitlab-selected-repositories", Title: "Only frontend", ProjectID: project.ID,
		Kind: "requirement", WorkflowID: wf.ID, AssigneeUserID: developer.ID, RepositoryIDs: []string{"frontend"}}
	if err := f.s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	task := requestJSONAuthed[domain.Task](t, f.h, developer, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{}, http.StatusCreated)
	t.Cleanup(func() { _ = f.e.Cancel(task.RunID) })
	run := waitRunStatus(t, f.s, task.RunID, "succeeded")
	if repositories := run.RepositoryContexts(); len(repositories) != 1 || repositories[0].RepositoryID != "frontend" {
		t.Fatalf("run repositories = %+v", repositories)
	}
	if readRequests.Load() == 0 || otherRequests.Load() != 0 {
		t.Fatalf("authenticated Git requests=%d, unselected requests=%d", readRequests.Load(), otherRequests.Load())
	}
	sshTestGit(t, selected, "rev-parse", "--verify", "refs/heads/feature/gitlab-sop")
}

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestRepositorySaveRejectsUnsafeCloneURL(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, cloneURL := range []string{"ext::bash -c id>/tmp/pwn_rce", "fd::7", "--upload-pack=id", "ssh://-oProxyCommand=id/a"} {
		rec := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
			"projectId": "platform", "name": "pwn", "gongfengProjectId": "example/pwn", "cloneUrl": cloneURL,
		}, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("cloneUrl %q = %d: %s", cloneURL, rec.Code, rec.Body.String())
		}
	}

	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	rec := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "platform", "name": "local", "gongfengProjectId": "example/local", "cloneUrl": "/srv/other-project",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-admin local cloneUrl = %d: %s", rec.Code, rec.Body.String())
	}
	for _, cloneURL := range []string{"http://127.0.0.1:9/repo.git", "https://169.254.169.254/latest/meta-data", "http://localhost:3000/repo.git", "git@[::1]:a/b.git"} {
		rec = cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
			"projectId": "platform", "name": "internal", "gongfengProjectId": "example/internal", "cloneUrl": cloneURL,
		}, true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("non-admin host-local cloneUrl %q = %d: %s", cloneURL, rec.Code, rec.Body.String())
		}
	}
	rec = cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "platform", "name": "ok", "gongfengProjectId": "example/ok", "cloneUrl": "git@git.example.com:example/ok.git",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("ssh cloneUrl = %d: %s", rec.Code, rec.Body.String())
	}
}

// Host-local clone URLs stay available to platform administrators and to
// deployments that run without authentication, as the change log promises.
func TestRepositorySaveAllowsHostLocalCloneURLForAdmins(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	hostLocal := []string{"http://127.0.0.1:9/repo.git", "git@[::1]:a/b.git", "http://localhost:3000/repo.git"}
	for i, cloneURL := range hostLocal {
		rec := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
			"projectId": "platform", "name": fmt.Sprintf("open-%d", i), "gongfengProjectId": fmt.Sprintf("example/open-%d", i), "cloneUrl": cloneURL,
		}, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("auth off host-local cloneUrl %q = %d: %s", cloneURL, rec.Code, rec.Body.String())
		}
	}

	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	if _, err := f.s.GrantPlatformAdmin(f.user.LoginName); err != nil {
		t.Fatal(err)
	}
	for i, cloneURL := range hostLocal {
		rec := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
			"projectId": "platform", "name": fmt.Sprintf("admin-%d", i), "gongfengProjectId": fmt.Sprintf("example/admin-%d", i), "cloneUrl": cloneURL,
		}, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("admin host-local cloneUrl %q = %d: %s", cloneURL, rec.Code, rec.Body.String())
		}
	}
	// Private ranges stay open to project owners for self-hosted Git servers.
	if err := f.s.RevokePlatformAdmin(f.user.LoginName); err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "platform", "name": "private", "gongfengProjectId": "example/private", "cloneUrl": "https://10.0.0.5/team/repo.git",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-admin private-range cloneUrl = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRepositoryCloneURLNeverCarriesCredentials(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, cloneURL := range []string{
		"https://user:s3cret-token@github.com/acme/app.git",
		"https://ghp_tokenAsUser@github.com/acme/app.git",
		"ssh://git:pw@git.example.com/acme/app.git",
	} {
		rec := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
			"projectId": "platform", "name": "leak", "gongfengProjectId": "example/leak", "cloneUrl": cloneURL,
		}, true)
		if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "s3cret-token") {
			t.Fatalf("cloneUrl with credentials %q = %d: %s", cloneURL, rec.Code, rec.Body.String())
		}
	}
	rec := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "platform", "name": "leak", "gongfengProjectId": "example/leak", "webUrl": "https://user:s3cret-token@github.com/acme/app",
	}, true)
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "s3cret-token") {
		t.Fatalf("webUrl with credentials = %d: %s", rec.Code, rec.Body.String())
	}
	// A row stored before this check must not leak through the API either.
	if err := f.s.PutRepository(domain.Repository{ID: "legacy-leak", Name: "legacy", ProjectID: "platform", Provider: "github", DefaultBranch: "main", WebURL: "https://user:s3cret-token@github.com/acme/app", CloneURL: "https://user:s3cret-token@github.com/acme/app.git"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/repositories", "/api/repositories/legacy-leak", "/api/bootstrap"} {
		rec := cvBRequest(t, f, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "s3cret-token") || !strings.Contains(rec.Body.String(), `"https://github.com/acme/app"`) || !strings.Contains(rec.Body.String(), "https://github.com/acme/app.git") {
			t.Fatalf("GET %s = %d leaks or drops the clone URL: %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestRunnerTaskListDropsCloneURLCredentials(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const legacyURL = "https://user:s3cret-token@github.com/acme/app.git"
	if err := f.s.PutRunnerTask(domain.RunnerTask{ID: "rt-legacy", Provider: "mock", Status: "queued", Invocation: &domain.RunnerInvocation{
		Repository:   &domain.RunnerRepository{CloneURL: legacyURL},
		Repositories: []domain.RunnerRepository{{CloneURL: legacyURL}},
	}}); err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodGet, "/api/runners/tasks", nil, true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "s3cret-token") || !strings.Contains(rec.Body.String(), "https://github.com/acme/app.git") {
		t.Fatalf("GET /api/runners/tasks = %d leaks or drops the clone URL: %s", rec.Code, rec.Body.String())
	}
	if stored, ok := f.s.RunnerTask("rt-legacy"); !ok || stored.Invocation.Repository.CloneURL != legacyURL {
		t.Fatalf("the claiming runner still needs the stored clone URL: %+v", stored.Invocation)
	}
}

func TestLegacyCloneURLCredentialsStayOutOfPromptAndRunViews(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: "legacy-flow", Name: "Legacy flow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	const legacyURL = "https://user:s3cret-token@github.com/acme/app.git"
	if err := f.s.PutRepository(domain.Repository{ID: "legacy-run", Name: "legacy", ProjectID: "platform", Provider: "github", GongfengProjectID: "acme/app", DefaultBranch: "main", WebURL: "https://user:s3cret-token@github.com/acme/app", CloneURL: legacyURL}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutIssue(domain.Issue{ID: "iss-legacy", ProjectID: "platform", Title: "Legacy repo", AssigneeType: "agent", AssigneeID: "developer", WorkflowID: "legacy-flow", RepositoryID: "legacy-run"}); err != nil {
		t.Fatal(err)
	}
	rec := cvBRequest(t, f, http.MethodPost, "/api/issues/iss-legacy/dispatch", map[string]any{}, true)
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "s3cret-token") {
		t.Fatalf("dispatch = %d leaks credentials: %s", rec.Code, rec.Body.String())
	}
	var task domain.Task
	for _, candidate := range f.s.Tasks() {
		if candidate.IssueID == "iss-legacy" {
			task = candidate
		}
	}
	run, ok := f.s.Run(task.RunID)
	if !ok || run.Repository.CloneURL != legacyURL {
		t.Fatalf("stored run must keep the clone URL it clones with: %+v", run.Repository)
	}
	if strings.Contains(run.Input, "s3cret-token") || !strings.Contains(run.Input, "clone URL: https://github.com/acme/app.git") {
		t.Fatalf("prompt carries clone URL credentials: %q", run.Input)
	}
	for _, path := range []string{"/api/runs", "/api/runs/" + run.ID, "/api/tasks", "/api/tasks/" + task.ID} {
		rec := cvBRequest(t, f, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "s3cret-token") || !strings.Contains(rec.Body.String(), "https://github.com/acme/app.git") {
			t.Fatalf("GET %s = %d leaks or drops the clone URL: %s", path, rec.Code, rec.Body.String())
		}
	}

	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: "legacy-generic", Name: "Legacy generic", Generic: true, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	project, ok := f.s.Project("platform")
	if !ok {
		t.Fatal("platform project missing")
	}
	project.DefaultRepositoryID = "legacy-run"
	if err := f.s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	rec = cvBRequest(t, f, http.MethodPost, "/api/runs", map[string]any{"workflowId": "legacy-generic", "input": "x", "projectId": "platform"}, true)
	if rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "s3cret-token") || !strings.Contains(rec.Body.String(), "https://github.com/acme/app.git") {
		t.Fatalf("POST /api/runs = %d leaks or drops the clone URL: %s", rec.Code, rec.Body.String())
	}
	var started domain.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if stored, ok := f.s.Run(started.ID); !ok || len(stored.Repositories) == 0 || stored.Repositories[0].CloneURL != legacyURL {
		t.Fatalf("stored manual run must keep the clone URL it clones with: %+v", stored.Repositories)
	}
}

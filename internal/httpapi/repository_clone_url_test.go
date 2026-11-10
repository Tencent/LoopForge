package httpapi

import (
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
	rec = cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]any{
		"projectId": "platform", "name": "ok", "gongfengProjectId": "example/ok", "cloneUrl": "git@git.example.com:example/ok.git",
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("ssh cloneUrl = %d: %s", rec.Code, rec.Body.String())
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
	// A row stored before this check must not leak through the API either.
	if err := f.s.PutRepository(domain.Repository{ID: "legacy-leak", Name: "legacy", ProjectID: "platform", Provider: "github", DefaultBranch: "main", CloneURL: "https://user:s3cret-token@github.com/acme/app.git"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/repositories", "/api/repositories/legacy-leak"} {
		rec := cvBRequest(t, f, http.MethodGet, path, nil, true)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "s3cret-token") || !strings.Contains(rec.Body.String(), "https://github.com/acme/app.git") {
			t.Fatalf("GET %s = %d leaks or drops the clone URL: %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestLegacyCloneURLCredentialsStayOutOfPromptAndRunViews(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutWorkflow(domain.WorkflowDefinition{ID: "legacy-flow", Name: "Legacy flow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	const legacyURL = "https://user:s3cret-token@github.com/acme/app.git"
	if err := f.s.PutRepository(domain.Repository{ID: "legacy-run", Name: "legacy", ProjectID: "platform", Provider: "github", GongfengProjectID: "acme/app", DefaultBranch: "main", CloneURL: legacyURL}); err != nil {
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
}

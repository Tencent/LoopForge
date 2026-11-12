package httpapi

import (
	"net/http"
	"testing"
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

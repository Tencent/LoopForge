package httpapi

import (
	"fmt"
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

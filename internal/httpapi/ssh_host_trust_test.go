package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"golang.org/x/crypto/ssh"
)

func TestPlatformGitSSHHostTrustAPI(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	admin := cvBNewHTTPFixture(t)
	if _, err := admin.s.GrantPlatformAdmin(admin.user.LoginName); err != nil {
		t.Fatal(err)
	}
	member := admin
	user := domain.User{LoginName: "ssh-trust-member"}
	if err := admin.s.PutUser(user); err != nil {
		t.Fatal(err)
	}
	member.user, _ = admin.s.UserByLoginName(user.LoginName)

	const path = "/api/platform/git-ssh-hosts"
	const host = "git.example.org:2222"
	privateKey, key := sshTestIdentity(t)
	input := repo.PlatformGitSSHHost{Host: "GIT.EXAMPLE.ORG:2222", PublicKeys: string(ssh.MarshalAuthorizedKey(key))}
	for _, action := range []struct{ method, path string }{
		{http.MethodGet, path},
		{http.MethodPost, path + "/preview"},
		{http.MethodPost, path},
		{http.MethodPut, path + "/" + host},
		{http.MethodDelete, path + "/" + host},
	} {
		if resp := cvBRequest(t, member, action.method, action.path, input, true); resp.Code != http.StatusForbidden {
			t.Fatalf("member %s %s: HTTP %d", action.method, action.path, resp.Code)
		}
	}
	req := authedRequest(t, httptest.NewRequest(http.MethodDelete, path+"/"+host, nil), admin.user)
	req.Header.Set("Origin", "https://unrelated.example.org")
	rec := httptest.NewRecorder()
	admin.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write: HTTP %d", rec.Code)
	}

	assertStatus := func(method, endpoint string, body any, status int, revision ...string) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := authedRequest(t, httptest.NewRequest(method, endpoint, bytes.NewReader(raw)), admin.user)
		req.Header.Set("Content-Type", "application/json")
		if len(revision) != 0 {
			req.Header.Set("If-Match", strconv.Quote(revision[0]))
		}
		resp := httptest.NewRecorder()
		admin.h.ServeHTTP(resp, req)
		if resp.Code != status {
			t.Fatalf("%s %s: HTTP %d, want %d (%s)", method, endpoint, resp.Code, status, resp.Body.String())
		}
		return resp.Body.Bytes()
	}
	preview := assertStatus(http.MethodPost, path+"/preview", input, http.StatusOK)
	var entry platformSSHHostView
	if err := json.Unmarshal(preview, &entry); err != nil || entry.Host != host ||
		len(entry.Fingerprints) != 1 || entry.Fingerprints[0] != ssh.FingerprintSHA256(key) {
		t.Fatalf("fingerprint preview = %+v, %v", entry, err)
	}
	if got := strings.TrimSpace(string(assertStatus(http.MethodGet, path, nil, http.StatusOK))); got != "[]" {
		t.Fatalf("preview changed the registry: %s", got)
	}
	assertStatus(http.MethodPost, path, repo.PlatformGitSSHHost{Host: host, PublicKeys: privateKey}, http.StatusBadRequest)
	assertStatus(http.MethodPost, path, repo.PlatformGitSSHHost{Host: "*.example.org", PublicKeys: input.PublicKeys}, http.StatusBadRequest)
	if err := json.Unmarshal(assertStatus(http.MethodPost, path, input, http.StatusCreated), &entry); err != nil || entry.Revision == "" {
		t.Fatalf("registration must return its revision: %+v, %v", entry, err)
	}
	assertStatus(http.MethodPost, path, input, http.StatusConflict)
	stale := entry.Revision
	oldInput := input
	_, rotated := sshTestIdentity(t)
	input.PublicKeys = string(ssh.MarshalAuthorizedKey(rotated))
	target := path + "/git.example.org%3A2222"
	assertStatus(http.MethodPut, target, input, http.StatusPreconditionRequired)
	assertStatus(http.MethodDelete, target, nil, http.StatusPreconditionRequired)
	assertStatus(http.MethodPut, target, input, http.StatusOK, stale)
	assertStatus(http.MethodPut, target, oldInput, http.StatusPreconditionFailed, stale)
	assertStatus(http.MethodDelete, target, nil, http.StatusPreconditionFailed, stale)

	var entries []platformSSHHostView
	if err := json.Unmarshal(assertStatus(http.MethodGet, path, nil, http.StatusOK), &entries); err != nil || len(entries) != 1 ||
		entries[0].Host != host || entries[0].Revision == "" || entries[0].Revision == stale ||
		len(entries[0].Fingerprints) != 1 || entries[0].Fingerprints[0] != ssh.FingerprintSHA256(rotated) {
		t.Fatalf("updated SSH hosts = %+v, %v", entries, err)
	}
	current := entries[0].Revision
	assertStatus(http.MethodDelete, target, nil, http.StatusNoContent, current)
	if err := json.Unmarshal(assertStatus(http.MethodPost, path, input, http.StatusCreated), &entry); err != nil || entry.Revision == current {
		t.Fatalf("recreated registration must get a new revision: %+v, %v", entry, err)
	}
	assertStatus(http.MethodDelete, target, nil, http.StatusPreconditionFailed, current)
	assertStatus(http.MethodDelete, target, nil, http.StatusNoContent, entry.Revision)
	if pins, err := admin.s.PlatformSSHKnownHosts(t.Context(), []string{host}); err != nil || len(pins) != 0 {
		t.Fatalf("removed host remains trusted: %q, %v", pins, err)
	}
}

func registerTestPlatformSSHHost(t *testing.T, f cvBHTTPFixture, remote, knownHosts string) {
	t.Helper()
	pins, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	_, _, key, _, _, err := ssh.ParseKnownHosts(pins)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GrantPlatformAdmin(f.user.LoginName); err != nil {
		t.Fatal(err)
	}
	resp := cvBRequest(t, f, http.MethodPost, "/api/platform/git-ssh-hosts", repo.PlatformGitSSHHost{
		Host: remote, PublicKeys: string(ssh.MarshalAuthorizedKey(key)),
	}, true)
	if resp.Code != http.StatusCreated {
		t.Fatalf("register platform SSH host: HTTP %d %s", resp.Code, resp.Body.String())
	}
}

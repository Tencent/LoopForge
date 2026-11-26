package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitegress"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
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
		{http.MethodDelete, path + "?host=" + host},
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

	broken := repo.PlatformGitSSHHost{Host: "broken.example.org:22", PublicKeys: input.PublicKeys + "malformed"}
	if err := admin.s.DB.Create(&broken).Error; err != nil {
		t.Fatal(err)
	}
	rawHosts := []string{"missing-port.example.org", "bad/endpoint\r"}
	const legacyRevision = "旧登记修订号"
	for _, raw := range rawHosts {
		if err := admin.s.DB.Create(&repo.PlatformGitSSHHost{Host: raw, PublicKeys: input.PublicKeys, Revision: legacyRevision}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var entries []platformSSHHostView
	if err := json.Unmarshal(assertStatus(http.MethodGet, path, nil, http.StatusOK), &entries); err != nil || len(entries) != 4 {
		t.Fatalf("SSH host list with a malformed row = %+v, %v", entries, err)
	}
	byHost := make(map[string]platformSSHHostView, len(entries))
	for _, entry := range entries {
		byHost[entry.Host] = entry
	}
	if invalid := byHost[broken.Host]; !invalid.Invalid || invalid.InvalidHost || invalid.PublicKeys != "" || len(invalid.Fingerprints) != 0 {
		t.Fatalf("malformed SSH host = %+v", invalid)
	}
	for _, raw := range rawHosts {
		invalid := byHost[raw]
		if !invalid.Invalid || !invalid.InvalidHost || invalid.PublicKeys != "" || len(invalid.Fingerprints) != 0 ||
			!strings.HasPrefix(invalid.Revision, platformSSHHostLegacyRevisionPrefix) {
			t.Fatalf("malformed host %q = %+v", raw, invalid)
		}
		deleteEndpoint := path + "?host=" + url.QueryEscape(raw)
		assertStatus(http.MethodDelete, deleteEndpoint, nil, http.StatusPreconditionFailed, "")
		assertStatus(http.MethodDelete, deleteEndpoint, nil, http.StatusNoContent, invalid.Revision)
	}
	updated := byHost[host]
	if updated.Host != host || updated.Invalid || updated.Revision == "" || updated.Revision == stale ||
		len(updated.Fingerprints) != 1 || updated.Fingerprints[0] != ssh.FingerprintSHA256(rotated) {
		t.Fatalf("updated SSH host = %+v", updated)
	}
	if pins, err := admin.s.PlatformSSHKnownHosts(t.Context(), []string{host, broken.Host}); err != nil ||
		!strings.Contains(string(pins), "[git.example.org]:2222 ") || strings.Contains(string(pins), "broken.example.org") {
		t.Fatalf("pins with a malformed row = %q, %v", pins, err)
	}
	brokenTarget := path + "/broken.example.org%3A22"
	var repaired platformSSHHostView
	if err := json.Unmarshal(assertStatus(http.MethodPut, brokenTarget, input, http.StatusOK, ""), &repaired); err != nil ||
		repaired.Invalid || repaired.Revision == "" || len(repaired.Fingerprints) != 1 {
		t.Fatalf("repaired SSH host = %+v, %v", repaired, err)
	}
	assertStatus(http.MethodDelete, brokenTarget, nil, http.StatusNoContent, repaired.Revision)
	current := updated.Revision
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

func TestPlatformGitSSHHostProbe(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv(gitegress.AllowCIDRsEnv, "")
	admin := cvBNewHTTPFixture(t)
	if _, err := admin.s.GrantPlatformAdmin(admin.user.LoginName); err != nil {
		t.Fatal(err)
	}
	remote, _, _, file := localSSHGit(t, nil)
	host, err := gitwork.SSHHostScope(remote)
	if err != nil {
		t.Fatal(err)
	}
	pins, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	_, _, key, _, _, err := ssh.ParseKnownHosts(pins)
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/platform/git-ssh-hosts"
	input := platformSSHHostInput{Host: remote, Probe: &platformSSHHostProbe{}}
	if resp := cvBRequest(t, admin, http.MethodPost, path, input, true); resp.Code != http.StatusBadRequest {
		t.Fatalf("save must require verified public keys: HTTP %d %s", resp.Code, resp.Body)
	}
	if resp := cvBRequest(t, admin, http.MethodPost, path+"/preview", input, true); resp.Code != http.StatusBadGateway {
		t.Fatalf("probe bypassed the platform egress policy: HTTP %d %s", resp.Code, resp.Body)
	}
	t.Setenv(gitegress.AllowCIDRsEnv, "127.0.0.1/32")
	resp := cvBRequest(t, admin, http.MethodPost, path+"/preview", input, true)
	var preview platformSSHHostView
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &preview) != nil || preview.Host != host ||
		preview.PublicKeys != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) ||
		len(preview.Fingerprints) != 1 || preview.Fingerprints[0] != ssh.FingerprintSHA256(key) {
		t.Fatalf("SSH host probe: HTTP %d %s", resp.Code, resp.Body)
	}
	if resp := cvBRequest(t, admin, http.MethodGet, path, nil, true); resp.Code != http.StatusOK || strings.TrimSpace(resp.Body.String()) != "[]" {
		t.Fatalf("probe changed the registry: HTTP %d %s", resp.Code, resp.Body)
	}
}

func TestPlatformGitSSHHostProbeWithRunner(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	admin := cvBNewHTTPFixture(t)
	if _, err := admin.s.GrantPlatformAdmin(admin.user.LoginName); err != nil {
		t.Fatal(err)
	}
	hub := quietRunnerHub()
	handler := &sshHostTrustHandlers{deps: &deps{repo: admin.s}, hosts: admin.s.SSHHostTrustRepo, runnerControls: hub}
	router := gin.New()
	router.POST("/preview", handler.preview)
	admin.h = router

	const runnerID = "probe-runner"
	const host = "git.example.org:2222"
	runner := domain.Runner{ID: runnerID, OwnerUserID: admin.user.ID + 1, Status: domain.RunnerStatusOnline}
	if err := admin.s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	input := platformSSHHostInput{Host: host, Probe: &platformSSHHostProbe{RunnerID: runnerID}}
	if resp := cvBRequest(t, admin, http.MethodPost, "/preview", input, true); resp.Code != http.StatusForbidden {
		t.Fatalf("probe through a Runner owned by someone else: HTTP %d %s", resp.Code, resp.Body)
	}
	runner.OwnerUserID = admin.user.ID
	if err := admin.s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	stale := testRunnerConn(runnerID)
	stale.ownerUserID = admin.user.ID + 1
	stale.supportsSSHHostKeyProbe = true
	registerTestConn(hub, stale)
	t.Cleanup(stale.close)
	if resp := cvBRequest(t, admin, http.MethodPost, "/preview", input, true); resp.Code != http.StatusConflict || len(stale.send) != 0 ||
		!strings.Contains(resp.Body.String(), "绑定已变更") {
		t.Fatalf("probe after Runner ownership changed: HTTP %d %s", resp.Code, resp.Body)
	}
	old := testRunnerConn(runnerID)
	old.ownerUserID = admin.user.ID
	registerTestConn(hub, old)
	t.Cleanup(old.close)
	if resp := cvBRequest(t, admin, http.MethodPost, "/preview", input, true); resp.Code != http.StatusConflict || len(old.send) != 0 {
		t.Fatalf("probe through an older Runner: HTTP %d %s", resp.Code, resp.Body)
	}

	conn := testRunnerConn(runnerID)
	conn.ownerUserID = admin.user.ID
	conn.supportsSSHHostKeyProbe = true
	registerTestConn(hub, conn)
	t.Cleanup(conn.close)
	_, key := sshTestIdentity(t)
	payload, err := json.Marshal(string(ssh.MarshalAuthorizedKey(key)))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req := authedRequest(t, httptest.NewRequest(http.MethodPost, "/preview", bytes.NewReader(body)), admin.user)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		router.ServeHTTP(res, req)
		close(done)
	}()
	select {
	case message := <-conn.send:
		if request := message.WorkspaceRequest; request == nil || request.Operation != domain.RunnerWorkspaceOperationSSHHostKeyProbe ||
			request.SSHHostKeyAddress != host || request.SSHHostKeyOwnerID != 0 {
			t.Fatalf("Runner received %+v", message)
		}
		hub.resolveWorkspace(old, message.RequestID, domain.RunnerWorkspaceResponse{Status: http.StatusBadGateway})
		hub.resolveWorkspace(conn, message.RequestID, domain.RunnerWorkspaceResponse{Status: http.StatusOK, Payload: payload})
	case <-time.After(3 * time.Second):
		t.Fatal("Runner never received the probe")
	}
	<-done
	var preview platformSSHHostView
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &preview) != nil || preview.Host != host ||
		len(preview.Fingerprints) != 1 || preview.Fingerprints[0] != ssh.FingerprintSHA256(key) {
		t.Fatalf("Runner SSH host probe: HTTP %d %s", res.Code, res.Body)
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

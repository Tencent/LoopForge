package httpapi

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestGitHTTPHostAuthorizationUsesTheInstanceAddress(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("NO_PROXY", "localhost")
	t.Setenv("no_proxy", "localhost")
	admin := cvBNewHTTPFixture(t)
	if _, err := admin.s.GrantPlatformAdmin(admin.user.LoginName); err != nil {
		t.Fatal(err)
	}
	member := admin
	if err := member.s.PutUser(domain.User{LoginName: "git-http-member"}); err != nil {
		t.Fatal(err)
	}
	member.user, _ = member.s.UserByLoginName("git-http-member")

	const token = "fixture-http-token"
	git := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != domain.GitTokenHTTPUser || password != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="Git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/team/app.git/info/refs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "0123456789012345678901234567890123456789\trefs/heads/main")
	}))
	t.Cleanup(git.Close)
	_, port, err := net.SplitHostPort(git.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	host := "localhost:" + port
	scope := "http://" + host
	const hostsAPI = "/api/platform/git-http-hosts"
	authorize := map[string]string{"host": scope}
	credentialsAPI := fmt.Sprintf("/api/users/%d/git-http-credentials", member.user.ID)
	credential := map[string]string{"host": scope, "name": "Internal Git", "token": token}

	if resp := cvBRequest(t, member, http.MethodPost, hostsAPI, authorize, true); resp.Code != http.StatusForbidden {
		t.Fatalf("member authorization: HTTP %d", resp.Code)
	}
	if resp := cvBRequest(t, member, http.MethodPut, credentialsAPI, credential, true); resp.Code != http.StatusForbidden {
		t.Fatalf("token before authorization: HTTP %d", resp.Code)
	}
	resp := cvBRequest(t, admin, http.MethodPost, hostsAPI, authorize, true)
	var saved repo.PlatformGitHTTPHost
	if resp.Code != http.StatusCreated || json.Unmarshal(resp.Body.Bytes(), &saved) != nil || saved.Host != host || saved.Revision == "" {
		t.Fatalf("authorization by host alone: HTTP %d %s", resp.Code, resp.Body)
	}
	if resp := cvBRequest(t, member, http.MethodGet, "/api/git-http-hosts", nil, true); resp.Code != http.StatusOK ||
		resp.Body.String() != fmt.Sprintf(`[{"host":%q,"platform":"other"}]`, scope) {
		t.Fatalf("choices: HTTP %d %s", resp.Code, resp.Body)
	}
	resp = cvBRequest(t, member, http.MethodPut, credentialsAPI, credential, true)
	var credentials []domain.GitHTTPCredential
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &credentials) != nil || len(credentials) != 1 || strings.Contains(resp.Body.String(), token) {
		t.Fatalf("save token: HTTP %d %s", resp.Code, resp.Body)
	}
	if got := member.s.GitHTTPWorkspaceHosts(member.user.ID)[scope]; got != domain.GitPlatformOther {
		t.Fatalf("workspace platform = %q", got)
	}
	verify := func(want bool) {
		t.Helper()
		resp := cvBRequest(t, member, http.MethodPost, credentialsAPI+"/"+credentials[0].ID+"/verify",
			map[string]string{"repoUrl": scope + "/team/app.git"}, true)
		var result gitTokenVerifyResult
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &result) != nil || result.RepositoryReadable != want {
			t.Fatalf("repository readable, want %t: HTTP %d %s", want, resp.Code, resp.Body)
		}
	}
	verify(true)

	req := authedRequest(t, httptest.NewRequest(http.MethodDelete, hostsAPI+"/"+host, nil), admin.user)
	req.Header.Set("If-Match", strconv.Quote(saved.Revision))
	resp = httptest.NewRecorder()
	admin.h.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("revoke: HTTP %d %s", resp.Code, resp.Body)
	}
	verify(false)
	if _, ok := member.s.GitHTTPCredentialForHost(t.Context(), member.user.ID, scope); ok {
		t.Fatal("a revoked authorization issued a token")
	}
	if _, ok := member.s.GitHTTPWorkspaceHosts(member.user.ID)[scope]; ok {
		t.Fatal("a revoked authorization appeared in the workspace")
	}
}

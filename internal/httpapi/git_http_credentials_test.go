package httpapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitegress"
)

func TestGitHTTPCredentialsSettings(t *testing.T) {
	t.Setenv("AGENTFLOW_GONGFENG_OAUTH_API_BASE", "https://git.internal.example/api/v3")
	f := cvBNewHTTPFixture(t)
	endpoint := fmt.Sprintf("/api/users/%d/git-http-credentials", f.user.ID)
	const secret = "ghp_settings_secret_value"
	if resp := cvBRequest(t, f, http.MethodGet, "/api/users/999/git-http-credentials", nil, true); resp.Code != http.StatusForbidden {
		t.Fatalf("another account's credentials returned HTTP %d", resp.Code)
	}
	put := func(input map[string]any, want int) []domain.GitHTTPCredential {
		t.Helper()
		resp := cvBRequest(t, f, http.MethodPut, endpoint, input, true)
		if resp.Code != want || strings.Contains(resp.Body.String(), secret) {
			t.Fatalf("PUT %v: HTTP %d %s", input, resp.Code, resp.Body.String())
		}
		var creds []domain.GitHTTPCredential
		if want == http.StatusOK && json.Unmarshal(resp.Body.Bytes(), &creds) != nil {
			t.Fatalf("decode %s", resp.Body.String())
		}
		return creds
	}

	creds := put(map[string]any{"host": "https://GitHub.com/", "name": "GitHub", "token": secret}, http.StatusOK)
	if len(creds) != 1 || creds[0].Host != "github.com" || creds[0].Platform != domain.GitPlatformGitHub {
		t.Fatalf("created credential = %+v", creds)
	}
	githubID := creds[0].ID
	put(map[string]any{"host": "github.com", "name": "Again", "token": "other"}, http.StatusConflict)
	put(map[string]any{"host": "http://github.com", "name": "Plain", "token": "x"}, http.StatusBadRequest)
	put(map[string]any{"host": "10.0.0.1", "name": "IP", "token": "x"}, http.StatusBadRequest)
	put(map[string]any{"host": "gitlab.example.org", "name": "No token"}, http.StatusBadRequest)
	put(map[string]any{"host": "gitlab.example.org", "name": "Spaces", "token": "a b"}, http.StatusBadRequest)
	put(map[string]any{"host": "gitlab.example.org", "name": "Colon", "username": "a:b", "token": "x"}, http.StatusBadRequest)
	put(map[string]any{"host": "gitlab.example.org", "name": "Bad", "platform": "bitbucket", "token": "x"}, http.StatusBadRequest)
	put(map[string]any{"id": githubID, "host": "gitlab.example.org", "name": "Moved"}, http.StatusNotFound)

	creds = put(map[string]any{"host": "gitlab.example.org:8443", "name": "Corp GitLab", "platform": "gitlab", "username": "oauth2", "token": "glpat-x", "exposeToWorkspace": false}, http.StatusOK)
	if len(creds) != 2 {
		t.Fatalf("credentials = %+v", creds)
	}
	creds = put(map[string]any{"id": githubID, "host": "github.com", "name": "GitHub renamed"}, http.StatusOK)
	if creds[1].Name != "GitHub renamed" {
		t.Fatalf("an edit without token or exposure must keep both: %+v", creds)
	}
	ctx := context.Background()
	if got, ok := f.s.GitHTTPCredentialForHost(ctx, f.user.ID, "github.com:443"); !ok || got.Token != secret || got.GitUsername() != domain.GitTokenHTTPUser {
		t.Fatalf("github.com credential = %+v, %v", got.GitHTTPCredential, ok)
	}
	if _, ok := f.s.GitHTTPCredentialForHost(ctx, f.user.ID, "gitlab.example.org"); ok {
		t.Fatal("a credential must match the port exactly")
	}
	if _, ok := f.s.GitHTTPCredentialForHost(ctx, f.user.ID+1, "github.com"); ok {
		t.Fatal("another user borrowed the credential")
	}
	hosts := f.s.GitHTTPWorkspaceHosts(f.user.ID)
	if len(hosts) != 2 || hosts["github.com"] != domain.GitPlatformGitHub {
		t.Fatalf("workspace hosts = %v; all configured credentials must be available", hosts)
	}
	if user, _ := f.s.User(f.user.ID); !user.GitHTTPCredentialsConfigured {
		t.Fatal("user must report configured HTTPS credentials")
	}

	if _, err := f.s.SaveGongfengOAuth(f.user.ID, "managed-access", "managed-refresh", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	resp := cvBRequest(t, f, http.MethodGet, endpoint, nil, true)
	if strings.Contains(resp.Body.String(), "managed-access") {
		t.Fatal("the managed token leaked into the description")
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &creds)
	managed := creds[len(creds)-1]
	if !managed.Managed || managed.ID != domain.ManagedGitHTTPCredentialID || managed.Host != "git.internal.example" {
		t.Fatalf("managed credential = %+v", managed)
	}
	got, ok := f.s.GitHTTPCredentialForHost(ctx, f.user.ID, "git.internal.example")
	if !ok || got.Token != "managed-access" || got.Username != "oauth2" || got.ExpiresAt.IsZero() {
		t.Fatalf("managed secret = %+v, %v", got.GitHTTPCredential, ok)
	}
	if _, ok := f.s.GitHTTPWorkspaceHosts(f.user.ID)["git.internal.example"]; !ok {
		t.Fatal("the managed credential must reach workspaces")
	}
	put(map[string]any{"id": domain.ManagedGitHTTPCredentialID, "host": "git.internal.example", "name": "Edit"}, http.StatusBadRequest)
	if resp := cvBRequest(t, f, http.MethodDelete, endpoint+"/"+domain.ManagedGitHTTPCredentialID, nil, true); resp.Code != http.StatusBadRequest {
		t.Fatalf("delete managed credential: HTTP %d", resp.Code)
	}
	creds = put(map[string]any{"host": "git.internal.example", "name": "Own PAT", "platform": "gitlab", "token": "own-pat"}, http.StatusOK)
	for _, cred := range creds {
		if cred.Managed {
			t.Fatal("a personal credential for the managed host must replace it")
		}
	}
	if got, _ := f.s.GitHTTPCredentialForHost(ctx, f.user.ID, "git.internal.example"); got.Token != "own-pat" {
		t.Fatal("the personal credential must take precedence over the managed one")
	}

	if resp := cvBRequest(t, f, http.MethodDelete, endpoint+"/"+githubID, nil, true); resp.Code != http.StatusOK {
		t.Fatalf("delete: HTTP %d", resp.Code)
	}
	if _, ok := f.s.GitHTTPCredentialForHost(ctx, f.user.ID, "github.com"); ok {
		t.Fatal("deleted credential still resolves")
	}
}

func TestVerifyGitHTTPCredential(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "ghe.example.com" && r.URL.Path == "/api/v3/user" && r.Header.Get("Authorization") == "Bearer ghe-token":
			_, _ = w.Write([]byte(`{"login":"octo"}`))
		case r.Host == "gitlab.example.com" && r.URL.Path == "/api/v4/user" && r.Header.Get("PRIVATE-TOKEN") == "gl-token":
			_, _ = w.Write([]byte(`{"username":"tanuki"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	previous := gitHTTPVerifyClient
	gitHTTPVerifyClient = func(gitegress.HTTPSHostLookup) *http.Client {
		transport := srv.Client().Transport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: transport.TLSClientConfig.RootCAs, ServerName: "example.com"}
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		}
		return &http.Client{Transport: transport}
	}
	defer func() { gitHTTPVerifyClient = previous }()

	f := cvBNewHTTPFixture(t)
	endpoint := fmt.Sprintf("/api/users/%d/git-http-credentials", f.user.ID)
	verify := func(host, platform, token string) gitTokenVerifyResult {
		t.Helper()
		resp := cvBRequest(t, f, http.MethodPut, endpoint, map[string]any{"host": host, "name": host, "platform": platform, "token": token}, true)
		var creds []domain.GitHTTPCredential
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &creds) != nil {
			t.Fatalf("save %s: HTTP %d", host, resp.Code)
		}
		id := ""
		for _, cred := range creds {
			if cred.Host == host {
				id = cred.ID
			}
		}
		resp = cvBRequest(t, f, http.MethodPost, endpoint+"/"+id+"/verify", nil, true)
		var result gitTokenVerifyResult
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &result) != nil || strings.Contains(resp.Body.String(), token) {
			t.Fatalf("verify %s: HTTP %d %s", host, resp.Code, resp.Body.String())
		}
		return result
	}
	if got := verify("ghe.example.com", "github", "ghe-token"); !got.Valid || got.Login != "octo" {
		t.Fatalf("GHE verify = %+v", got)
	}
	if got := verify("gitlab.example.com", "gitlab", "gl-token"); !got.Valid || got.Login != "tanuki" {
		t.Fatalf("GitLab verify = %+v", got)
	}
	if got := verify("bad.example.com", "gitlab", "wrong"); got.Valid || !strings.Contains(got.Error, "401") {
		t.Fatalf("rejected token verify = %+v", got)
	}
	if got := verify("gitea.example.com", "other", "tea"); got.Valid || got.Error == "" {
		t.Fatalf("other platform verify = %+v", got)
	}
	if resp := cvBRequest(t, f, http.MethodPost, endpoint+"/missing/verify", nil, true); resp.Code != http.StatusNotFound {
		t.Fatalf("unknown credential verify: HTTP %d", resp.Code)
	}
}

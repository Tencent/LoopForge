package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	"github.com/gin-gonic/gin"
)

func TestPlatformGitHTTPSHostAuthorization(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	admin := cvBNewHTTPFixture(t)
	if _, err := admin.s.GrantPlatformAdmin(admin.user.LoginName); err != nil {
		t.Fatal(err)
	}
	member := admin
	if err := member.s.PutUser(domain.User{LoginName: "git-https-member"}); err != nil {
		t.Fatal(err)
	}
	member.user, _ = member.s.UserByLoginName("git-https-member")

	const path = "/api/platform/git-https-hosts"
	const host = "gitlab.example.org:8443"
	const choicesPath = "/api/git-https-hosts"
	input := repo.PlatformGitHTTPSHost{Host: "HTTPS://GitLab.Example.org:8443/", Platform: "gitlab"}
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

	firstIP, secondIP := netip.MustParseAddr("10.2.3.4"), netip.MustParseAddr("fd00::2")
	previewResponse := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(previewResponse)
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	c.Request = authedRequest(t, httptest.NewRequest(http.MethodPost, path+"/preview", bytes.NewReader(raw)), admin.user)
	previewHandler := &httpsHostTrustHandlers{
		deps: &deps{repo: admin.s},
		lookupIP: func(_ context.Context, network, name string) ([]netip.Addr, error) {
			if network != "ip" || name != "gitlab.example.org" {
				t.Fatalf("unexpected DNS lookup %s %s", network, name)
			}
			return []netip.Addr{secondIP, firstIP, firstIP}, nil
		},
	}
	previewHandler.preview(c)
	var preview repo.PlatformGitHTTPSHost
	if previewResponse.Code != http.StatusOK || json.Unmarshal(previewResponse.Body.Bytes(), &preview) != nil ||
		preview.Host != host || !slices.Equal(preview.Addresses, []netip.Addr{firstIP, secondIP}) {
		t.Fatalf("preview: HTTP %d %s", previewResponse.Code, previewResponse.Body)
	}
	manual := input
	manual.Addresses = []netip.Addr{secondIP, netip.MustParseAddr("::ffff:10.2.3.4"), firstIP}
	manualResponse := cvBRequest(t, admin, http.MethodPost, path+"/preview", manual, true)
	var manualPreview repo.PlatformGitHTTPSHost
	if manualResponse.Code != http.StatusOK || json.Unmarshal(manualResponse.Body.Bytes(), &manualPreview) != nil ||
		manualPreview.Host != host || !slices.Equal(manualPreview.Addresses, preview.Addresses) {
		t.Fatalf("manual preview: HTTP %d %s", manualResponse.Code, manualResponse.Body)
	}
	if resp := cvBRequest(t, member, http.MethodGet, choicesPath, nil, true); resp.Code != http.StatusOK || resp.Body.String() != "[]" {
		t.Fatalf("preview must leave choices empty: HTTP %d %s", resp.Code, resp.Body)
	}

	for _, addresses := range [][]netip.Addr{
		{},
		{netip.MustParseAddr("127.0.0.1")},
		{netip.MustParseAddr("169.254.1.1")},
		{netip.MustParseAddr("ff02::1")},
	} {
		input.Addresses = addresses
		for _, endpoint := range []string{path, path + "/preview"} {
			if resp := cvBRequest(t, admin, http.MethodPost, endpoint, input, true); resp.Code != http.StatusBadRequest {
				t.Fatalf("invalid addresses %v for %s: HTTP %d", addresses, endpoint, resp.Code)
			}
		}
	}
	resp := cvBRequest(t, admin, http.MethodPost, path, preview, true)
	var saved repo.PlatformGitHTTPSHost
	if resp.Code != http.StatusCreated || json.Unmarshal(resp.Body.Bytes(), &saved) != nil || saved.Revision == "" {
		t.Fatalf("save: HTTP %d %s", resp.Code, resp.Body)
	}
	if resp := cvBRequest(t, member, http.MethodGet, choicesPath, nil, true); resp.Code != http.StatusOK ||
		resp.Body.String() != `[{"host":"gitlab.example.org:8443","platform":"gitlab"}]` {
		t.Fatalf("choices must contain only host and provider: HTTP %d %s", resp.Code, resp.Body)
	}
	checkAddresses := func(target string, want []netip.Addr) {
		t.Helper()
		got, err := admin.s.PlatformGitHTTPSAddresses(t.Context(), target)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("authorized addresses for %q = %v, %v; want %v", target, got, err, want)
		}
	}
	checkAddresses("GITLAB.EXAMPLE.ORG:8443", preview.Addresses)
	checkAddresses("gitlab.example.org:443", nil)

	revised := func(method string, entry repo.PlatformGitHTTPSHost, revision string) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		req := authedRequest(t, httptest.NewRequest(method, path+"/"+host, bytes.NewReader(raw)), admin.user)
		req.Header.Set("If-Match", strconv.Quote(revision))
		resp := httptest.NewRecorder()
		admin.h.ServeHTTP(resp, req)
		return resp
	}
	saved.Addresses = []netip.Addr{secondIP}
	resp = revised(http.MethodPut, saved, saved.Revision)
	var updated repo.PlatformGitHTTPSHost
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &updated) != nil || updated.Revision == saved.Revision {
		t.Fatalf("update: HTTP %d %s", resp.Code, resp.Body)
	}
	checkAddresses(host, updated.Addresses)
	if resp := revised(http.MethodDelete, saved, saved.Revision); resp.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale revoke: HTTP %d %s", resp.Code, resp.Body)
	}
	if resp := revised(http.MethodDelete, updated, updated.Revision); resp.Code != http.StatusNoContent {
		t.Fatalf("revoke: HTTP %d %s", resp.Code, resp.Body)
	}
	checkAddresses(host, nil)
}

func TestPlatformGitHTTPSIPv6Authorization(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	admin := cvBNewHTTPFixture(t)
	if _, err := admin.s.GrantPlatformAdmin(admin.user.LoginName); err != nil {
		t.Fatal(err)
	}
	member := admin
	if err := member.s.PutUser(domain.User{LoginName: "ipv6-git-member"}); err != nil {
		t.Fatal(err)
	}
	member.user, _ = member.s.UserByLoginName("ipv6-git-member")

	const endpoint = "/api/platform/git-https-hosts"
	const scope = "[fd00::2]:8443"
	const expanded = "[FD00:0:0:0:0:0:0:2]:8443"
	wantIP := netip.MustParseAddr("fd00::2")
	for input, wantScope := range map[string]string{"fd00::2": "[fd00::2]", "https://" + expanded + "/": scope} {
		resp := cvBRequest(t, admin, http.MethodPost, endpoint+"/preview", map[string]string{"host": input, "platform": "gitlab"}, true)
		var preview repo.PlatformGitHTTPSHost
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &preview) != nil ||
			preview.Host != wantScope || !slices.Equal(preview.Addresses, []netip.Addr{wantIP}) {
			t.Fatalf("preview %s: HTTP %d %s", input, resp.Code, resp.Body)
		}
	}
	for _, input := range []map[string]any{
		{"host": scope, "platform": "gitlab", "addresses": []string{"fd00::3"}},
		{"host": "fe80::2", "platform": "gitlab", "addresses": []string{"fe80::2"}},
		{"host": "::1", "platform": "gitlab", "addresses": []string{"::1"}},
	} {
		for _, path := range []string{endpoint, endpoint + "/preview"} {
			if resp := cvBRequest(t, admin, http.MethodPost, path, input, true); resp.Code != http.StatusBadRequest {
				t.Fatalf("invalid IPv6 authorization at %s: HTTP %d %s", path, resp.Code, resp.Body)
			}
		}
	}
	if resp := cvBRequest(t, admin, http.MethodPost, endpoint, repo.PlatformGitHTTPSHost{
		Host: expanded, Platform: "gitlab", Addresses: []netip.Addr{wantIP},
	}, true); resp.Code != http.StatusCreated {
		t.Fatalf("save IPv6 authorization: HTTP %d %s", resp.Code, resp.Body)
	}
	if got, err := admin.s.PlatformGitHTTPSAddresses(t.Context(), expanded); err != nil || !slices.Equal(got, []netip.Addr{wantIP}) {
		t.Fatalf("load IPv6 authorization: %v %v", got, err)
	}
	if got, err := admin.s.PlatformGitHTTPSAddresses(t.Context(), "[fd00::2]:443"); err != nil || got != nil {
		t.Fatalf("different port got IPv6 authorization: %v %v", got, err)
	}
	if resp := cvBRequest(t, member, http.MethodGet, "/api/git-https-hosts", nil, true); resp.Code != http.StatusOK ||
		resp.Body.String() != `[{"host":"[fd00::2]:8443","platform":"gitlab"}]` {
		t.Fatalf("IPv6 instance choice: HTTP %d %s", resp.Code, resp.Body)
	}

	path := "/api/users/" + strconv.FormatInt(member.user.ID, 10) + "/git-http-credentials"
	if resp := cvBRequest(t, member, http.MethodPut, path, map[string]string{
		"host": expanded, "platform": "gitlab", "name": "IPv6 GitLab", "token": "fake-ipv6-token",
	}, true); resp.Code != http.StatusOK {
		t.Fatalf("save IPv6 credential: HTTP %d %s", resp.Code, resp.Body)
	}
	if cred, ok := admin.s.GitHTTPCredentialForHost(t.Context(), member.user.ID, scope); !ok ||
		cred.Host != scope || cred.Token != "fake-ipv6-token" || cred.APIBase() != "https://[fd00::2]:8443/api/v4" {
		t.Fatalf("wrong IPv6 credential: %+v (found %v)", cred.GitHTTPCredential, ok)
	}
	if _, ok := admin.s.GitHTTPCredentialForHost(t.Context(), member.user.ID, "fd00::2"); ok {
		t.Fatal("IPv6 credential was returned for another port")
	}
}

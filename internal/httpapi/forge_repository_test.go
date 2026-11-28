package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/forge"
)

func TestForgejoRepositoryBinding(t *testing.T) {
	const web = "https://forge.example.org:8443/code/team/app"
	const remote = "ssh://git@forge.example.org:2222/team/app.git"
	t.Setenv(forge.InstancesEnv, "https://forge.example.org:8443/code")
	f := cvBNewHTTPFixture(t)
	t.Run("forgejo", func(t *testing.T) {
		const provider = domain.RepositoryProviderForgejo
		input := map[string]string{"projectId": "platform", "provider": provider, "repoUrl": remote, "webUrl": web}
		resp := cvBRequest(t, f, http.MethodPost, "/api/repositories", input, true)
		var saved domain.Repository
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &saved) != nil ||
			saved.Provider != provider || saved.WebURL != web || saved.CloneURL != remote ||
			saved.GongfengProjectID != "forge:"+web || saved.DefaultBranch != "main" {
			t.Fatalf("%s binding: HTTP %d %s", provider, resp.Code, resp.Body)
		}
		input["id"], input["url"] = saved.ID, web+"/other"
		if got := cvBRequest(t, f, http.MethodPut, "/api/repositories", input, true); got.Code != http.StatusBadRequest {
			t.Fatalf("mismatched secondary URL: HTTP %d", got.Code)
		}
		input["url"], input["webUrl"] = "", "https://unlisted.example.org/team/app"
		if got := cvBRequest(t, f, http.MethodPut, "/api/repositories", input, true); got.Code != http.StatusBadRequest {
			t.Fatalf("unlisted instance: HTTP %d", got.Code)
		}
		if stored, _ := f.s.Repository(saved.ID); stored.WebURL != web || stored.CloneURL != remote {
			t.Fatal("invalid update changed a repository")
		}
	})
}

func TestForgeCredentialsArePrivateAndScoped(t *testing.T) {
	const one = "https://forge.example.org/one"
	const two = "https://forge.example.org/two"
	const secret = "test-personal-forge-credential"
	t.Setenv(forge.InstancesEnv, one+","+two)
	f := cvBNewHTTPFixture(t)
	if err := f.s.SaveForgeToken(f.user.ID, one, secret); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SaveForgeToken(f.user.ID, two, "test-other-token"); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("/api/users/%d/forge-tokens", f.user.ID)
	res := cvBRequest(t, f, http.MethodGet, url, nil, true)
	var got struct {
		Tokens []struct{ ID, Instance string }
	}
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" ||
		json.Unmarshal(res.Body.Bytes(), &got) != nil || len(got.Tokens) != 2 ||
		strings.Contains(res.Body.String(), secret) || got.Tokens[0].Instance != one {
		t.Fatalf("invalid credential metadata response: HTTP %d", res.Code)
	}
	if f.s.ForgeToken(f.user.ID+1, one) != "" || f.s.ForgeToken(f.user.ID, one+"/subpath") != "" {
		t.Fatal("a token was selectable by another user or instance")
	}
	denied := fmt.Sprintf("/api/users/%d/forge-tokens/%s", f.user.ID+1, got.Tokens[0].ID)
	if res := cvBRequest(t, f, http.MethodDelete, denied, nil, true); res.Code != http.StatusForbidden {
		t.Fatalf("other user's credential is accessible: HTTP %d", res.Code)
	}
	if res := cvBRequest(t, f, http.MethodPut, url, map[string]string{"instance": "https://unlisted.example.org", "token": secret}, true); res.Code != http.StatusBadRequest || strings.Contains(res.Body.String(), secret) {
		t.Fatalf("unlisted instance: HTTP %d", res.Code)
	}
	if res := cvBRequest(t, f, http.MethodDelete, url+"/"+got.Tokens[0].ID, nil, true); res.Code != http.StatusOK ||
		f.s.ForgeToken(f.user.ID, one) != "" || f.s.ForgeToken(f.user.ID, two) != "test-other-token" {
		t.Fatal("deletion did not preserve the other instance")
	}
}

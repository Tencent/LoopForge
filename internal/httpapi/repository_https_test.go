package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
)

func TestGenericHTTPRepositoryNeedsNoHostingAdapter(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, test := range []struct{ provider, address string }{
		{domain.RepositoryProviderHTTPS, "https://unfamiliar.example.org/team/code.git"},
		{domain.RepositoryProviderHTTP, "http://192.0.2.10:8080/team/code.git"},
		{domain.RepositoryProviderHTTP, "http://github.com/team/code.git"},
	} {
		resp := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]string{"projectId": "platform", "repoUrl": test.address}, true)
		var r domain.Repository
		if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &r) != nil || r.Provider != test.provider || r.CloneURL != test.address || r.GongfengProjectID != "" {
			t.Fatalf("generic repository %s: HTTP %d %s", test.address, resp.Code, resp.Body.String())
		}
	}
	for _, address := range []string{"https://user:token@unfamiliar.example.org/team/code", "https://unfamiliar.example.org/team/code?token=x", "https://unfamiliar.example.org/team/../code"} {
		if response := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]string{"projectId": "platform", "repoUrl": address}, true); response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe URL accepted: %s", address)
		}
	}
}

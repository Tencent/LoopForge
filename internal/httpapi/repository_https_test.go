package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
)

func TestUnknownHTTPSRepositoryNeedsNoHostingAdapter(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	resp := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]string{"projectId": "platform", "repoUrl": "https://unfamiliar.example.org/team/code.git"}, true)
	var r domain.Repository
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &r) != nil || r.Provider != domain.RepositoryProviderHTTPS || r.CloneURL != "https://unfamiliar.example.org/team/code.git" || r.GongfengProjectID != "" {
		t.Fatalf("generic HTTPS repository: HTTP %d %s", resp.Code, resp.Body.String())
	}
	for _, address := range []string{"https://user:token@unfamiliar.example.org/team/code", "https://unfamiliar.example.org/team/code?token=x", "https://unfamiliar.example.org/team/../code"} {
		if response := cvBRequest(t, f, http.MethodPost, "/api/repositories", map[string]string{"projectId": "platform", "repoUrl": address}, true); response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe URL accepted: %s", address)
		}
	}
}

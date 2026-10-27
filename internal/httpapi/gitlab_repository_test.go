package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"net/http"
	"path/filepath"
	"testing"
)

func TestGitLabRepositoryBindingRequiresConfiguredOrigin(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutUser(domain.User{LoginName: "gitlab-owner", GitLabBaseURL: "http://git.example.test:9530", GitLabToken: "test-token"}); err != nil {
		t.Fatal(err)
	}
	u, _ := s.UserByLoginName("gitlab-owner")
	if err := s.PutProject(domain.Project{ID: "p", Name: "P", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	saved := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{"projectId": "p", "repoUrl": "http://git.example.test:9530/group/sub/repo.git"}, http.StatusOK)
	if saved.Provider != "gitlab" || saved.GongfengProjectID != "gitlab:http://git.example.test:9530/group/sub/repo" {
		t.Fatalf("wrong repository: %+v", saved)
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/repositories", map[string]any{"projectId": "p", "repoUrl": "http://git.example.test.attacker:9530/group/repo.git"}, http.StatusBadRequest)
}

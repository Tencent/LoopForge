package tracker

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitLabMRUsesConfiguredOwnerAndChecksMerge(t *testing.T) {
	merged := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "gitlab-secret" {
			t.Error("wrong token")
		}
		if !strings.Contains(r.URL.EscapedPath(), "team%2Fsub%2Frepo") {
			t.Errorf("path not encoded: %s", r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			if !strings.Contains(r.URL.Path, "/merge_requests") {
				w.Write([]byte(`{"id":42,"path_with_namespace":"team/sub/repo"}`))
				return
			}
			w.Write([]byte(`[{"iid":7,"source_project_id":42,"target_project_id":42,"state":"opened","source_branch":"feature","target_branch":"main","sha":"abc","web_url":"http://example.invalid/mr/7"}]`))
		case "PUT":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["sha"] != "abc" {
				t.Error("missing observed sha")
			}
			state := "opened"
			if merged {
				state = "merged"
			}
			json.NewEncoder(w).Encode(map[string]any{"iid": 7, "state": state, "source_project_id": 42, "target_project_id": 42, "source_branch": "feature", "target_branch": "main", "sha": "abc"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	service, project := gitLabTestService(t, srv.URL)
	if _, err := service.EnsureAndMergeBranchMR(project, "feature", "main", "", ""); err == nil {
		t.Fatal("must not claim merge without confirmation")
	}
	merged = true
	if ref, err := service.EnsureAndMergeBranchMR(project, "feature", "main", "", ""); err != nil || ref != "!7" {
		t.Fatalf("merge %q: %v", ref, err)
	}
}

func gitLabTestService(t *testing.T, base string) (*Service, string) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutUser(domain.User{LoginName: "gitlab-owner", GitLabToken: "gitlab-secret", GitLabBaseURL: base}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("gitlab-owner")
	if err := st.PutProject(domain.Project{ID: "p", Name: "p", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	project := "gitlab:" + base + "/team/sub/repo"
	if err := st.PutRepository(domain.Repository{ID: "r", ProjectID: "p", Provider: "gitlab", Name: "repo", GongfengProjectID: project}); err != nil {
		t.Fatal(err)
	}
	service := NewService()
	service.ConfigureWithStore(domain.SyncConfig{}, st)
	return service, project
}

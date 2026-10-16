package tracker

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubImportPreservesLocalWorkAndMergeChecksConfirmation(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "github-owner", GitToken: "project-owner-token"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("github-owner")
	if err := st.PutProject(domain.Project{ID: "public", Name: "Public", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	r := domain.Repository{ID: "github-repo", ProjectID: "public", Provider: "github", Name: "repo", GongfengProjectID: "github:example/repo", DefaultBranch: "main", WebURL: "https://github.com/example/repo"}
	if err := st.PutRepository(r); err != nil {
		t.Fatal(err)
	}
	mergeAllowed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer project-owner-token" {
			t.Errorf("wrong actor credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo/issues":
			_, _ = w.Write([]byte(`[{"id":10,"number":2,"title":"Remote issue","body":"Original brief","state":"open","html_url":"https://github.com/example/repo/issues/2"},{"id":11,"number":3,"title":"PR","pull_request":{}}]`))
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo/pulls":
			_, _ = w.Write([]byte(`[{"number":7,"state":"open","head":{"ref":"feature/work","sha":"expected-head"},"base":{"ref":"main"}}]`))
		case r.Method == "PUT" && r.URL.Path == "/repos/example/repo/pulls/7/merge":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["sha"] != "expected-head" {
				t.Error("merge must bind to the observed head")
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"merged": mergeAllowed})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.githubAPIBase = srv.URL
	if n, err := s.pullGitHubIssues(st, r); err != nil || n != 1 {
		t.Fatalf("import: %d %v", n, err)
	}
	issue, ok := st.Issue("gh-github-repo-2")
	if !ok || issue.Source != "github" {
		t.Fatalf("issue: %+v", issue)
	}
	issue.Description = "Locally clarified requirement"
	issue.Status = "in_progress"
	if err := st.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pullGitHubIssues(st, r); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Issue(issue.ID); got.Description != issue.Description || got.Status != "in_progress" {
		t.Fatalf("local work overwritten: %+v", got)
	}
	if _, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "feature/work", "main", "Title", ""); err == nil {
		t.Fatal("HTTP success without merged=true was reported as merged")
	}
	mergeAllowed = true
	if ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "feature/work", "main", "Title", ""); err != nil || ref != "#7" {
		t.Fatalf("merge: %s %v", ref, err)
	}
}

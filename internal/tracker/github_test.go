package tracker

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestGitHubPullMergedRefAndDeliveryState(t *testing.T) {
	at := time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		pull   githubPull
		merged bool
		state  string
	}{
		{"merged flag", githubPull{Number: 7, Merged: true, State: "closed"}, true, "merged"},
		{"merged_at only", githubPull{Number: 7, MergedAt: &at, State: "closed"}, true, "merged"},
		{"closed unmerged", githubPull{Number: 7, State: "closed"}, false, "closed"},
		{"open", githubPull{Number: 7, State: "open"}, false, "opened"},
	}
	for _, c := range cases {
		if got := c.pull.merged(); got != c.merged {
			t.Fatalf("%s: merged()=%v want %v", c.name, got, c.merged)
		}
		if got := c.pull.delivery("github:example/repo").State; got != c.state {
			t.Fatalf("%s: delivery state=%q want %q", c.name, got, c.state)
		}
		if got := c.pull.ref(); got != "#7" {
			t.Fatalf("%s: ref()=%q", c.name, got)
		}
	}
}

func TestGitHubMergeBranchShortCircuitsMergedPullAndReopensClosed(t *testing.T) {
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
	pulls := map[string]string{
		"merged-flag": `[{"number":11,"state":"closed","merged":true,"head":{"ref":"merged-flag","sha":"a"},"base":{"ref":"main"}}]`,
		"merged-at":   `[{"number":12,"state":"closed","merged_at":"2026-11-13T04:00:00Z","head":{"ref":"merged-at","sha":"b"},"base":{"ref":"main"}}]`,
		"closed":      `[{"number":13,"state":"closed","head":{"ref":"closed","sha":"c"},"base":{"ref":"main"}}]`,
	}
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/example/repo/pulls":
			head := r.URL.Query().Get("head")
			if r.URL.Query().Get("base") != "main" {
				t.Errorf("findPull must filter by target base, got %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(pulls[head[len("example:"):]]))
		case r.Method == "POST" && r.URL.Path == "/repos/example/repo/pulls":
			writes = append(writes, "POST")
			_, _ = w.Write([]byte(`{"number":21,"state":"open","head":{"ref":"closed","sha":"new-head"},"base":{"ref":"main"}}`))
		case r.Method == "PUT" && r.URL.Path == "/repos/example/repo/pulls/21/merge":
			writes = append(writes, "PUT 21")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["sha"] != "new-head" {
				t.Errorf("merge must bind to the reopened pull head, got %v", body["sha"])
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"merged": true})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := NewService()
	s.ConfigureWithStore(domain.SyncConfig{}, st)
	s.githubAPIBase = srv.URL

	for branch, want := range map[string]string{"merged-flag": "#11", "merged-at": "#12"} {
		ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, branch, "main", "Title", "")
		if err != nil || ref != want {
			t.Fatalf("%s: ref=%q err=%v want %q", branch, ref, err, want)
		}
	}
	if len(writes) != 0 {
		t.Fatalf("already merged pulls must not be re-created or re-merged: %v", writes)
	}
	ref, err := s.EnsureAndMergeBranchMR(r.GongfengProjectID, "closed", "main", "", "")
	if err != nil || ref != "#21" {
		t.Fatalf("closed unmerged pull: ref=%q err=%v want #21", ref, err)
	}
	if len(writes) != 2 || writes[0] != "POST" || writes[1] != "PUT 21" {
		t.Fatalf("closed unmerged pull must be re-opened then merged: %v", writes)
	}
}

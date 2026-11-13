package tracker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestWriteBackClarificationToGitHubIssueRejectsNonGitHubRepositoryAndSurfacesAPIErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer srv.Close()
	github := domain.Repository{ID: "gh-repo", ProjectID: "alpha", Provider: "github", GongfengProjectID: "github:example/repo"}
	gongfeng := domain.Repository{ID: "gf-repo", ProjectID: "alpha", Provider: "gongfeng", GongfengProjectID: "group/repo"}
	blank := domain.Repository{ID: "gh-blank", ProjectID: "alpha", Provider: "github", GongfengProjectID: "  "}
	svc := NewService()
	svc.ConfigureWithStore(domain.SyncConfig{}, projectAuthFixture{
		projects: map[string]domain.Project{"alpha": {ID: "alpha", OwnerID: 1}},
		users:    map[int64]domain.User{1: {ID: 1, LoginName: "owner", GitToken: "owner-token"}},
		repos:    []domain.Repository{github, gongfeng, blank},
	})
	svc.githubAPIBase = srv.URL
	st := &memIssueStore{repositories: map[string]domain.Repository{"gh-repo": github, "gf-repo": gongfeng, "gh-blank": blank}}
	issue := domain.Issue{ID: "gh-gh-repo-12", Source: "github", RepositoryID: "gh-repo", ExternalRef: &domain.ExternalRef{Source: "github", ExternalID: "9001", ExternalIID: 12}}

	for _, repositoryID := range []string{"gf-repo", "gh-blank"} {
		wrong := issue
		wrong.RepositoryID = repositoryID
		if err := svc.WriteBackClarification(st, wrong, "x"); err == nil {
			t.Fatalf("GitHub write-back bound to repository %q must fail", repositoryID)
		}
	}
	negative := issue
	negative.ExternalRef = &domain.ExternalRef{Source: "github", ExternalID: "9001", ExternalIID: -3}
	if err := svc.WriteBackClarification(st, negative, "x"); err == nil {
		t.Fatal("GitHub write-back with a negative issue number must fail")
	}
	if calls != 0 {
		t.Fatalf("rejected write-backs must not call GitHub, calls=%d", calls)
	}

	if err := svc.WriteBackClarification(st, issue, "x"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("GitHub API failure must be returned to the caller, err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1", calls)
	}
}

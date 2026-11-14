package tracker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// These tests pin the behavior #842 moved out of PullAll into
// pullRepositoryIssues / recordPullResult: the GitHub sync log, provider
// dispatch, Gongfeng auth wrapping, and how PullAll merges and records errors.

// orderedRepoStore fixes the repository iteration order so PullAll's
// "last repository error wins" rule is observable.
type orderedRepoStore struct {
	*memIssueStore
	repos []domain.Repository
}

func (o *orderedRepoStore) Repositories() []domain.Repository { return o.repos }

var pullAllGitHubRepo = domain.Repository{ID: "gh-repo", ProjectID: "public", Provider: "github", GongfengProjectID: "github:example/repo", DefaultBranch: "main"}

func newPullAllStore(repos ...domain.Repository) *orderedRepoStore {
	return &orderedRepoStore{
		memIssueStore: &memIssueStore{issues: map[string]domain.Issue{}, repositories: map[string]domain.Repository{}, projects: map[string]domain.Project{}},
		repos:         repos,
	}
}

func githubIssuesServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/example/repo/issues" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`[{"id":11,"number":1,"title":"A","state":"open"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
}

func TestPullRepositoryIssuesGitHubLogsOutcome(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := githubIssuesServer(t, http.StatusOK)
		defer srv.Close()
		svc := NewService()
		svc.githubAPIBase = srv.URL

		if err := svc.pullRepositoryIssues(newPullAllStore(), pullAllGitHubRepo); err != nil {
			t.Fatalf("pullRepositoryIssues() = %v", err)
		}
		logs := svc.Logs()
		if len(logs) != 1 {
			t.Fatalf("want one GitHub sync log, got %+v", logs)
		}
		got := logs[0]
		if got.Direction != "pull" || got.Source != "github" || got.Action != "sync_issues" || got.Status != "success" || got.ProjectID != "public" {
			t.Fatalf("GitHub success log = %+v", got)
		}
		if got.Message != "GitHub: imported 1 work items" {
			t.Fatalf("GitHub success message = %q", got.Message)
		}
	})

	t.Run("failure", func(t *testing.T) {
		srv := githubIssuesServer(t, http.StatusInternalServerError)
		defer srv.Close()
		svc := NewService()
		svc.githubAPIBase = srv.URL

		err := svc.pullRepositoryIssues(newPullAllStore(), pullAllGitHubRepo)
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("pullRepositoryIssues() = %v, want the GitHub HTTP 500 error", err)
		}
		logs := svc.Logs()
		if len(logs) != 1 {
			t.Fatalf("want one GitHub sync log, got %+v", logs)
		}
		got := logs[0]
		if got.Source != "github" || got.Action != "sync_issues" || got.Status != "failed" || got.ProjectID != "public" || got.Message != err.Error() {
			t.Fatalf("GitHub failure log = %+v (err %v)", got, err)
		}
	})
}

func TestPullRepositoryIssuesSkipsNonGongfengProviderWithProjectID(t *testing.T) {
	// A non-GitHub/non-Gongfeng repository that still carries a project path
	// must not be routed to the (unconfigured) Gongfeng client.
	svc := NewService()
	if err := svc.pullRepositoryIssues(newPullAllStore(), domain.Repository{ID: "repo-local", ProjectID: "platform", Provider: "local", GongfengProjectID: "group/repo"}); err != nil {
		t.Fatalf("local repository must be skipped, got %v", err)
	}
	if logs := svc.Logs(); len(logs) != 0 {
		t.Fatalf("skipped repository must not log, got %+v", logs)
	}
}

func TestPullRepositoryIssuesWrapsGongfengPullErrorWithAuthHint(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{GongfengMCPURL: gateway.URL})
	repository := domain.Repository{ID: "repo-gf", ProjectID: "platform", Provider: "gongfeng", GongfengProjectID: "test/repo"}

	client, source, err := svc.trackerClientForProject("gongfeng", repository.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	_, rawErr := svc.pullGongfengIssues(client, newPullAllStore(), repository)
	if rawErr == nil {
		t.Fatal("expected the 401 gateway to fail the Gongfeng pull")
	}
	want := svc.authError("工蜂", source, rawErr)
	if want == rawErr.Error() {
		t.Fatalf("fixture must produce an auth-rewritten message, got %q", want)
	}

	err = svc.pullRepositoryIssues(newPullAllStore(), repository)
	if err == nil || err.Error() != want {
		t.Fatalf("pullRepositoryIssues() = %v, want %q", err, want)
	}
}

func TestPullAllKeepsLastRepositoryErrorAndAppendsTAPDError(t *testing.T) {
	srv := githubIssuesServer(t, http.StatusInternalServerError)
	defer srv.Close()
	svc := NewService() // no Gongfeng/TAPD client configured
	svc.githubAPIBase = srv.URL
	store := newPullAllStore(
		pullAllGitHubRepo,
		domain.Repository{ID: "repo-gf", ProjectID: "platform", Provider: "gongfeng", GongfengProjectID: "test/repo"},
	)
	store.projects["dp"] = domain.Project{ID: "dp", TAPDWorkspaceID: "10000000", TAPDIterationID: "it1"}

	err := svc.PullAll(store)
	want := "gongfeng client not configured；tapd client not configured"
	if err == nil || err.Error() != want {
		t.Fatalf("PullAll() = %v, want %q (last repository error, then TAPD error)", err, want)
	}
	cfg := svc.cfg.Get()
	if cfg.LastSyncStatus != "failed" || cfg.LastSyncError != want || cfg.LastSyncAt.IsZero() {
		t.Fatalf("PullAll must record its merged error: %+v", cfg)
	}
}

func TestPullAllRecordsGitHubFailure(t *testing.T) {
	srv := githubIssuesServer(t, http.StatusInternalServerError)
	defer srv.Close()
	svc := NewService()
	svc.githubAPIBase = srv.URL

	err := svc.PullAll(newPullAllStore(pullAllGitHubRepo))
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("PullAll() = %v, want the GitHub repository error", err)
	}
	if cfg := svc.cfg.Get(); cfg.LastSyncStatus != "failed" || cfg.LastSyncError != err.Error() {
		t.Fatalf("PullAll must record the GitHub failure: %+v", cfg)
	}

	// The same pull against a healthy gateway clears the failure.
	ok := githubIssuesServer(t, http.StatusOK)
	defer ok.Close()
	svc.githubAPIBase = ok.URL
	if err := svc.PullAll(newPullAllStore(pullAllGitHubRepo)); err != nil {
		t.Fatalf("PullAll() = %v", err)
	}
	if cfg := svc.cfg.Get(); cfg.LastSyncStatus != "success" || cfg.LastSyncError != "" {
		t.Fatalf("PullAll must record the recovered pull: %+v", cfg)
	}
}

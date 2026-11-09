package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitLabCredentialBoundPerRepository(t *testing.T) {
	base := "http://git.example.test:9530"
	gitlab := domain.RepositoryContext{RepositoryID: "gl", Provider: "gitlab", CloneURL: base + "/team/repo.git"}
	github := domain.RepositoryContext{RepositoryID: "gh", Provider: "github", CloneURL: "https://github.com/team/repo.git"}
	run := domain.Run{Repositories: []domain.RepositoryContext{gitlab, github}}
	ctx := withGitLabCredentials(withGitToken(context.Background(), "github-secret"), base, "gitlab-secret")
	auth, ok := gitwork.HostCredentialFrom(gitContextForRepository(ctx, gitlab))
	if !ok || auth.Token != "gitlab-secret" {
		t.Fatal("GitLab credential missing")
	}
	if _, ok := gitwork.HostCredentialFrom(gitContextForRepository(ctx, github)); ok {
		t.Fatal("GitHub got GitLab host credential")
	}
	inv := &domain.RunnerInvocation{}
	bindRunnerRepositories(inv, run, "github-secret", "", "")
	bindRunnerGitLabCredentials(inv, run, base, "gitlab-secret")
	if inv.Repositories[0].GitToken != "gitlab-secret" || inv.Repositories[0].GitAuthBaseURL != base || inv.Repositories[1].GitToken != "github-secret" || inv.Repositories[1].GitAuthBaseURL != "" {
		t.Fatal("runner credentials are not separated")
	}
}

func TestGitLabRunnerRequiresHostBoundCredentialSupport(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, r := range []domain.Runner{{ID: "old", RunnerVersion: "0.9.2"}, {ID: "new", RunnerVersion: domain.RunnerVersionGitLabCredentials}} {
		if err := st.PutRunner(r); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{repo: st}
	repositories := []domain.RepositoryContext{{Provider: "gitlab"}}
	if err := e.requireWorkBranchCapabilities("old", repositories); err == nil {
		t.Fatal("old runner accepted a GitLab task")
	}
	if err := e.requireWorkBranchCapabilities("new", repositories); err != nil {
		t.Fatal(err)
	}
}

func TestGitLabConversationPushDoesNotUseGitHubToken(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run := domain.Run{ID: "gitlab-conversation", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Repositories: []domain.RepositoryContext{{RepositoryID: "gl", Provider: "gitlab", CloneURL: "https://gitlab.example.test/team/repo.git", WorkBranch: "feature"}}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	heads, err := e.PushConversationWorkspace(context.Background(), run.ID, "github-secret", "alice", "alice@example.test", "change", "")
	if err == nil || !strings.Contains(err.Error(), "GitLab conversation delivery") || len(heads) != 0 {
		t.Fatalf("unsupported delivery was not blocked: %v %v", heads, err)
	}
}

func TestGitLabRunCredentialsSkipMismatchedRepository(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutUser(domain.User{LoginName: "gitlab-owner", GitToken: "github-secret", GitLabToken: "gitlab-secret", GitLabBaseURL: "https://gitlab.example.test"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("gitlab-owner")
	run := domain.Run{ExecutionContext: domain.TelemetryContext{ResponsibleUserID: user.ID}, Repositories: []domain.RepositoryContext{{RepositoryID: "wrong", Provider: "gitlab", CloneURL: "https://other.example.test/team/repo.git"}, {RepositoryID: "right", Provider: "gitlab", CloneURL: "https://gitlab.example.test/team/repo.git"}}}
	e := &Engine{repo: st}
	credentials := e.resolveRunCredentials(run)
	if credentials.GitLabToken != "gitlab-secret" || credentials.GitToken != "github-secret" {
		t.Fatal("matching repository did not receive isolated credentials")
	}
	ctx := withGitLabCredentials(context.Background(), credentials.GitLabBaseURL, credentials.GitLabToken)
	auth, ok := gitwork.HostCredentialFrom(gitContextForRepository(ctx, run.Repositories[0]))
	if !ok || auth.Token != "" {
		t.Fatal("mismatched repository received a credential")
	}
}

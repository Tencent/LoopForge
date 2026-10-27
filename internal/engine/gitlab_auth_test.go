package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"context"
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
	for _, r := range []domain.Runner{{ID: "old", RunnerVersion: "0.7.0"}, {ID: "new", RunnerVersion: "0.8.0"}} {
		if err := st.PutRunner(r); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{repo: st}
	repositories := []domain.RepositoryContext{{Provider: "gitlab"}}
	if err := e.requireExistingWorkBranchRunner("old", repositories); err == nil {
		t.Fatal("old runner accepted a GitLab task")
	}
	if err := e.requireExistingWorkBranchRunner("new", repositories); err != nil {
		t.Fatal(err)
	}
}

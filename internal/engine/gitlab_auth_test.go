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
	"time"
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
	if err == nil || !strings.Contains(err.Error(), "GitLab Token") || len(heads) != 0 {
		t.Fatalf("missing requester GitLab credentials were not blocked: %v %v", heads, err)
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

func TestGitLabConversationRunnerUsesRequesterCredentials(t *testing.T) {
	e, st := cvEEngine(t)
	runner := cvERunner(t, e, "gitlab-runner", domain.RunnerStatusOnline, "terminal")
	runner.RunnerVersion = domain.RunnerVersionGitLabCredentials
	if err := st.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	run := cvERunnerRun("gitlab-push", runner.ID)
	run.ExecutionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared
	run.Repositories = []domain.RepositoryContext{
		{RepositoryID: "gl", Name: "GL", Provider: "gitlab", CloneURL: "https://gitlab.example.test/team/repo.git", GongfengProjectID: "gitlab:https://gitlab.example.test/team/repo", WorkBranch: "feature", WorkspaceDir: "gl"},
		{RepositoryID: "gh", Name: "GH", Provider: "github", CloneURL: "https://github.com/team/repo.git", WorkBranch: "feature", WorkspaceDir: "gh"},
		{RepositoryID: "read-only", Name: "readonly", Provider: "gitlab", CloneURL: "https://other.example.test/team/repo.git", WorkBranch: "feature", WorkspaceDir: "readonly", ReadOnly: true},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.SetRunnerDispatchNotify(func(string) {
		calls++
		tasks := st.RunnerTasks()
		if len(tasks) != 1 {
			t.Fatalf("tasks=%d", len(tasks))
		}
		task := tasks[0]
		if task.Invocation == nil || len(task.Invocation.Repositories) != 2 {
			t.Fatalf("invocation=%+v", task.Invocation)
		}
		gl, gh := task.Invocation.Repositories[0], task.Invocation.Repositories[1]
		if gl.GitAuthBaseURL != "https://gitlab.example.test" || gh.GitAuthBaseURL != "" {
			t.Fatal("provider credentials not scoped")
		}
		if gl.GitToken != "requester-gitlab" || gh.GitToken != "github-secret" {
			t.Fatal("persisted task lost requester credentials")
		}
		leased, ok, err := st.ClaimRunnerTask(runner.ID, []string{"terminal"}, time.Now().UTC(), time.Minute)
		if err != nil || !ok || leased.Invocation == nil {
			t.Fatalf("claim: %v %v", ok, err)
		}
		if leased.Invocation.Repositories[0].GitToken != "requester-gitlab" || leased.Invocation.Repositories[1].GitToken != "github-secret" {
			t.Fatal("lease lost requester credentials")
		}
		if gl.SharedPushTarget != "preview" || gh.SharedPushTarget != "preview" {
			t.Fatal("direct target lost")
		}
		cvEFinishQueuedTask(t, e, domain.RunnerTaskSuccess, `[{"repositoryId":"gl","workBranch":"feature","head":"abc","sharedBranch":"preview","sharedHead":"def"}]`, "", nil)
	})
	creds := ConversationGitLabCredentials{BaseURL: "https://gitlab.example.test", Token: "requester-gitlab"}
	if _, err := e.PushConversationWorkspace(context.Background(), run.ID, "github-secret", "alice", "alice@example.test", "title", "preview", creds); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("dispatches=%d", calls)
	}
}

func TestGitLabConversationPreflightsAllCredentials(t *testing.T) {
	for _, tc := range []struct{ name, base, token, project, generic string }{
		{name: "missing GitLab token", base: "https://gitlab.example.test", project: "gitlab:https://gitlab.example.test/team/repo", generic: "github-token"},
		{name: "wrong instance", base: "https://other.example.test", token: "gitlab-token", project: "gitlab:https://gitlab.example.test/team/repo", generic: "github-token"},
		{name: "wrong project", base: "https://gitlab.example.test", token: "gitlab-token", project: "gitlab:https://gitlab.example.test/team/other", generic: "github-token"},
		{name: "missing generic token", base: "https://gitlab.example.test", token: "gitlab-token", project: "gitlab:https://gitlab.example.test/team/repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, st := cvEEngine(t)
			runner := cvERunner(t, e, "gitlab-runner", domain.RunnerStatusOnline, "terminal")
			runner.RunnerVersion = domain.RunnerVersionGitLabCredentials
			st.PutRunner(runner)
			run := cvERunnerRun("preflight", runner.ID)
			run.Repositories = []domain.RepositoryContext{
				{RepositoryID: "gh", Provider: "github", CloneURL: "https://github.com/team/repo.git", WorkBranch: "feature"},
				{RepositoryID: "gl", Provider: "gitlab", CloneURL: "https://gitlab.example.test/team/repo.git", GongfengProjectID: tc.project, WorkBranch: "feature"},
			}
			st.PutRun(run)
			if _, err := e.PushConversationWorkspace(context.Background(), run.ID, tc.generic, "alice", "alice@example.test", "title", "", ConversationGitLabCredentials{BaseURL: tc.base, Token: tc.token}); err == nil {
				t.Fatal("invalid credentials accepted")
			}
			if len(st.RunnerTasks()) != 0 {
				t.Fatal("dispatched before preflight completed")
			}
		})
	}
}

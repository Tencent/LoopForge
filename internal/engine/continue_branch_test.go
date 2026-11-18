package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestContinueExistingBranchKeepsNameAndUnpublishedWork(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	_, remote := selectedBranchTestRemote(t)
	workspace := filepath.Join(t.TempDir(), "checkout")
	repository := domain.RepositoryContext{RepositoryID: "repo", Name: "Service", CloneURL: remote, BaseBranch: "master", DefaultBranch: "master", WorkBranch: "feature/continue", RequireExistingWorkBranch: true}
	if err := prepareRepository(context.Background(), workspace, repository); err != nil {
		t.Fatal(err)
	}
	branch, err := gitC(context.Background(), workspace, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != "feature/continue" {
		t.Fatalf("must continue the requested branch without generating another name: %q, %v", branch, err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "version.txt"))
	if err != nil || string(content) != "existing feature work\n" {
		t.Fatalf("existing branch work was lost: %q, %v", content, err)
	}
	mustGit(t, workspace, "config", "--local", "user.name", "AgentFlow Test")
	mustGit(t, workspace, "config", "--local", "user.email", "agentflow-test@example.com")
	gitCommitFile(t, workspace, "continued.txt", "new work on the existing branch\n")
	head, err := gitC(context.Background(), workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareRepository(context.Background(), workspace, repository); err != nil {
		t.Fatal(err)
	}
	resumed, err := gitC(context.Background(), workspace, "rev-parse", "HEAD")
	if err != nil || resumed != head {
		t.Fatalf("resuming must retain unpublished work on the requested branch: %q, %v", resumed, err)
	}
	mustGit(t, "", "--git-dir", remote, "update-ref", "-d", "refs/heads/feature/continue")
	if err := prepareRepository(context.Background(), workspace, repository); err == nil {
		t.Fatal("a deleted existing branch must fail even when a cached remote ref remains")
	}
}

func TestContinueExistingBranchRejectsMissingOrTagOnlyRef(t *testing.T) {
	t.Parallel()
	source, remote := selectedBranchTestRemote(t)
	mustGit(t, source, "tag", "release-tag")
	mustGit(t, source, "push", remote, "refs/tags/release-tag")
	for _, branch := range []string{"feature/missing", "release-tag"} {
		repository := domain.RepositoryContext{RepositoryID: "repo", CloneURL: remote, BaseBranch: "master", DefaultBranch: "master", WorkBranch: branch, RequireExistingWorkBranch: true}
		if err := prepareRepository(context.Background(), filepath.Join(t.TempDir(), "checkout"), repository); err == nil {
			t.Fatalf("%q must not fall back to creating a new work branch", branch)
		}
	}
}

func TestContinueExistingBranchResumePreservesDirtyNamesakeCheckout(t *testing.T) {
	t.Parallel()
	source, remote := selectedBranchTestRemote(t)
	mustGit(t, source, "branch", "-f", "feature/continue", "master")
	mustGit(t, source, "push", "--force", remote, "feature/continue")
	workspace := filepath.Join(t.TempDir(), "checkout")
	repository := domain.RepositoryContext{RepositoryID: "repo", CloneURL: remote, BaseBranch: "master", DefaultBranch: "master", WorkBranch: "feature/continue", RequireExistingWorkBranch: true}
	if err := prepareRepository(context.Background(), workspace, repository); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "version.txt"), []byte("uncommitted user work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, source, "checkout", "feature/continue")
	gitCommitFile(t, source, "remote-change.txt", "someone else advanced this branch\n")
	mustGit(t, source, "push", remote, "feature/continue")
	if err := prepareRepository(context.Background(), workspace, repository); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "version.txt"))
	if err != nil || string(content) != "uncommitted user work\n" {
		t.Fatalf("resuming discarded uncommitted user work: %q, %v", content, err)
	}
}

func TestContinueExistingBranchMRUsesSameSourceAndConfiguredTarget(t *testing.T) {
	t.Parallel()
	run := domain.Run{Repositories: []domain.RepositoryContext{{RepositoryID: "repo", Name: "Service", BaseBranch: "release/main", WorkBranch: "feature/continue", RequireExistingWorkBranch: true}}}
	hint := existingMRBlurb(nil, run)
	for _, want := range []string{"不新建", "已有 MR", "feature/continue", "release/main"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("continuation MR guidance missing %q: %s", want, hint)
		}
	}
}

func TestContinueExistingBranchRequiresCompatibleRunner(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutRunner(domain.Runner{ID: "old", RunnerVersion: "0.6.0"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRunner(domain.Runner{ID: "new", RunnerVersion: domain.RunnerVersionExistingWorkBranch}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{repo: st}
	repositories := []domain.RepositoryContext{{RepositoryID: "repo", WorkBranch: "feature/continue", RequireExistingWorkBranch: true}}
	if err := e.requireWorkBranchCapabilities("old", repositories); err == nil {
		t.Fatal("an older runner must not silently ignore the existing-branch requirement")
	}
	if err := e.requireWorkBranchCapabilities("new", repositories); err != nil {
		t.Fatal(err)
	}
	repositories[0].RequireExistingWorkBranch = false
	if err := e.requireWorkBranchCapabilities("old", repositories); err != nil {
		t.Fatalf("creating a new branch must stay compatible with older runners: %v", err)
	}
}

func TestCreateWorkBranchRequiresCompatibleRunner(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutRunner(domain.Runner{ID: "old", RunnerVersion: "0.9.0"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRunner(domain.Runner{ID: "new", RunnerVersion: domain.RunnerVersionCreateNewWorkBranch}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{repo: st}
	repositories := []domain.RepositoryContext{{RepositoryID: "repo", WorkBranch: "feature/caller", CreateNewWorkBranch: true}}
	if err := e.requireWorkBranchCapabilities("old", repositories); err == nil {
		t.Fatal("an older runner must not silently ignore the create-branch requirement")
	}
	if err := e.requireWorkBranchCapabilities("new", repositories); err != nil {
		t.Fatal(err)
	}
}

func TestCNBRepositoryRequiresCNBCapableRunner(t *testing.T) {
	t.Parallel()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutRunner(domain.Runner{ID: "old", RunnerVersion: "0.10.1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRunner(domain.Runner{ID: "new", RunnerVersion: domain.RunnerVersionCNB}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{repo: st}
	cnb := []domain.RepositoryContext{{RepositoryID: "cnb", CloneURL: "https://cnb.cool/org/repo.git"}}
	if err := e.requireWorkBranchCapabilities("old", cnb); err == nil || !strings.Contains(err.Error(), domain.RunnerVersionCNB) {
		t.Fatalf("an old runner must be refused with the upgrade version, got %v", err)
	}
	if err := e.requireWorkBranchCapabilities("new", cnb); err != nil {
		t.Fatal(err)
	}
	other := []domain.RepositoryContext{{RepositoryID: "gh", CloneURL: "https://github.com/org/repo.git"}}
	if err := e.requireWorkBranchCapabilities("old", other); err != nil {
		t.Fatalf("other hosts must stay dispatchable to old runners: %v", err)
	}
}

func TestBindRunnerRepositoriesKeepsGlobalTokenOffCNB(t *testing.T) {
	t.Parallel()
	run := domain.Run{ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 9}, Repositories: []domain.RepositoryContext{
		{RepositoryID: "cnb", CloneURL: "https://cnb.cool/org/repo.git"},
		{RepositoryID: "gh", CloneURL: "https://github.com/org/repo.git"},
	}}
	invocation := &domain.RunnerInvocation{}
	bindRunnerRepositories(invocation, run, "ghp_FAKEGLOBALPAT", "", "")
	byID := map[string]domain.RunnerRepository{}
	for _, repository := range invocation.Repositories {
		byID[repository.RepositoryID] = repository
	}
	if byID["cnb"].GitToken != "" || !byID["cnb"].ScopedHTTPS {
		t.Fatalf("CNB checkout carries the global token: %+v", byID["cnb"])
	}
	if byID["gh"].GitToken != "ghp_FAKEGLOBALPAT" || byID["gh"].ScopedHTTPS {
		t.Fatalf("other hosts lost their token: %+v", byID["gh"])
	}
	if invocation.GitHTTPSUserID != 9 {
		t.Fatalf("GitHTTPSUserID = %d, want the responsible user", invocation.GitHTTPSUserID)
	}
}

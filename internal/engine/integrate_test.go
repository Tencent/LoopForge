package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestScheduledIntegrationSerializesAndReturnsConflictToWorkBranch(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "app.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "base")
	testGit(t, source, "branch", "agentflow/integration")
	cmd := exec.Command("git", "clone", "--bare", source, remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}

	cloneWork := func(name string) string {
		dir := filepath.Join(root, name)
		cmd := exec.Command("git", "clone", "--branch", "agentflow/integration", remote, dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clone %s: %v: %s", name, err, out)
		}
		testGit(t, dir, "config", "user.email", "test@example.com")
		testGit(t, dir, "config", "user.name", "Test")
		testGit(t, dir, "checkout", "-b", name)
		return dir
	}
	first, second := cloneWork("work-one"), cloneWork("work-two")
	if err := os.WriteFile(filepath.Join(first, "app.txt"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "app.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	firstRepo := domain.RepositoryContext{WorkBranch: "work-one", IntegrateBranch: "agentflow/integration"}
	if conflicts, err := mergeLatestIntegration(ctx, first, firstRepo, "issue #1 first"); err != nil || len(conflicts) != 0 {
		t.Fatalf("first integration conflicts=%v err=%v", conflicts, err)
	}
	testGit(t, first, "push", "origin", "HEAD:refs/heads/agentflow/integration")
	secondRepo := domain.RepositoryContext{WorkBranch: "work-two", IntegrateBranch: "agentflow/integration"}
	conflicts, err := mergeLatestIntegration(ctx, second, secondRepo, "issue #2 second")
	if err != nil || len(conflicts) != 1 || conflicts[0] != "app.txt" {
		t.Fatalf("second integration conflicts=%v err=%v", conflicts, err)
	}
	if testGit(t, second, "branch", "--show-current") != "work-two" {
		t.Fatal("conflict was not retained on the SOP work branch")
	}
	if err := os.WriteFile(filepath.Join(second, "app.txt"), []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, second, "add", "app.txt")
	if conflicts, err = mergeLatestIntegration(ctx, second, secondRepo, "issue #2 second"); err != nil || len(conflicts) != 0 {
		t.Fatalf("resolved integration conflicts=%v err=%v", conflicts, err)
	}
	testGit(t, second, "push", "origin", "HEAD:refs/heads/agentflow/integration")
}

func TestMergeLatestIntegrationAutoResolvesSOPDeliverableConflicts(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "fix-plan.md"), []byte("# plan v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "base with sop report")
	testGit(t, source, "branch", "agentflow/integration")
	testGit(t, source, "checkout", "agentflow/integration")
	testGit(t, source, "rm", "fix-plan.md")
	testGit(t, source, "commit", "-m", "drop sop reports from baseline")
	if out, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}

	work := filepath.Join(root, "work")
	if out, err := exec.Command("git", "clone", "--branch", "master", remote, work).CombinedOutput(); err != nil {
		t.Fatalf("clone work: %v: %s", err, out)
	}
	testGit(t, work, "config", "user.email", "test@example.com")
	testGit(t, work, "config", "user.name", "Test")
	testGit(t, work, "checkout", "-b", "feature/fix-issue-31")
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nfunc Fix() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "fix-plan.md"), []byte("# plan v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, work, "add", ".")
	testGit(t, work, "commit", "-m", "issue work plus updated sop report")

	ctx := context.Background()
	repo := domain.RepositoryContext{WorkBranch: "feature/fix-issue-31", IntegrateBranch: "agentflow/integration"}
	conflicts, err := mergeLatestIntegration(ctx, work, repo, "issue #31")
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("SOP report conflicts should auto-resolve: conflicts=%v err=%v", conflicts, err)
	}
	if tracked := testGit(t, work, "ls-files", "--", "fix-plan.md"); tracked != "" {
		t.Fatalf("SOP report still tracked after integrate merge: %q", tracked)
	}
	if !strings.Contains(testGit(t, work, "show", "HEAD:app.go"), "func Fix") {
		t.Fatal("business change was lost while resolving SOP conflicts")
	}
}

func TestIntegrationConflictRequeuesRepairNodeAndDescendants(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	wf := domain.WorkflowDefinition{ID: "wf", Nodes: []domain.WorkflowNode{
		{ID: "implement", Type: "agent"}, {ID: "review", Type: "agent", RejectTo: "implement"}, {ID: "mr", Type: "agent"},
	}, Edges: []domain.WorkflowEdge{{Source: "implement", Target: "review"}, {Source: "review", Target: "mr"}}}
	run := domain.Run{ID: "run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running", Repository: domain.RepositoryContext{IntegrateBranch: "agentflow/integration"}, Nodes: map[string]*domain.NodeRun{
		"implement": {NodeID: "implement", Status: "succeeded"}, "review": {NodeID: "review", Status: "succeeded"}, "mr": {NodeID: "mr", Status: "succeeded"},
	}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	if err := e.requeueIntegrationConflict(run.ID, "release/2026", []string{"app.go"}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Run(run.ID)
	for _, id := range []string{"implement", "review", "mr"} {
		if got.Nodes[id].Status != "pending" {
			t.Fatalf("node %s status=%s", id, got.Nodes[id].Status)
		}
	}
	if got.Repository.IntegrationReworks != 1 || !strings.Contains(got.Nodes["implement"].Feedback, "app.go") || !strings.Contains(got.Nodes["implement"].Feedback, "release/2026") {
		t.Fatalf("unexpected conflict feedback: %+v", got.Repository)
	}
}

func TestScheduledIntegrationPreparesConfiguredMainBranchWithoutDirectPush(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "release")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "app.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "base")
	testGit(t, source, "branch", "agentflow/integration")
	if out, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}

	workspace := filepath.Join(root, "workspace")
	if out, err := exec.Command("git", "clone", "--branch", "agentflow/integration", remote, workspace).CombinedOutput(); err != nil {
		t.Fatalf("clone workspace: %v: %s", err, out)
	}
	testGit(t, workspace, "config", "user.email", "test@example.com")
	testGit(t, workspace, "config", "user.name", "Test")
	testGit(t, workspace, "checkout", "-b", "scheduled-work")
	if err := os.WriteFile(filepath.Join(workspace, "scheduled.txt"), []byte("delivered\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	repo := domain.RepositoryContext{
		WorkBranch:      "scheduled-work",
		IntegrateBranch: "agentflow/integration",
		DefaultBranch:   "release",
	}
	if conflicts, err := mergeLatestIntegration(ctx, workspace, repo, "scheduled-work"); err != nil || len(conflicts) != 0 {
		t.Fatalf("merge integration conflicts=%v err=%v", conflicts, err)
	}
	if conflicts, err := mergeLatestRemoteBranch(ctx, workspace, repo.DefaultBranch); err != nil || len(conflicts) != 0 {
		t.Fatalf("merge main branch conflicts=%v err=%v", conflicts, err)
	}
	testGit(t, workspace, "push", "origin", "HEAD:refs/heads/"+repo.IntegrateBranch)

	integrationSHA := strings.Fields(testGit(t, workspace, "ls-remote", "origin", "refs/heads/"+repo.IntegrateBranch))[0]
	mainSHA := strings.Fields(testGit(t, workspace, "ls-remote", "origin", "refs/heads/"+repo.DefaultBranch))[0]
	if integrationSHA == mainSHA {
		t.Fatal("protected main branch was updated directly instead of through an MR")
	}
}

func TestPrepareRepositoryDoesNotRealignBaseline(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "shared")
	testGit(t, source, "branch", "agentflow/integration")
	if err := os.WriteFile(filepath.Join(source, "mainline.txt"), []byte("from-master\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "master moved")
	if out, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}

	ctx := domain.RepositoryContext{
		RepositoryID:    "skillhub",
		CloneURL:        remote,
		BaseBranch:      "agentflow/integration",
		IntegrateBranch: "agentflow/integration",
		DefaultBranch:   "master",
		WorkBranch:      "feature/fix-issue-31",
	}
	integrateBefore := strings.Fields(testGit(t, remote, "rev-parse", "agentflow/integration"))[0]
	masterBefore := strings.Fields(testGit(t, remote, "rev-parse", "master"))[0]
	if err := prepareRepository(context.Background(), workspace, ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "mainline.txt")); !os.IsNotExist(err) {
		t.Fatal("workspace prepare must not merge default into the work branch")
	}
	if after := strings.Fields(testGit(t, remote, "rev-parse", "agentflow/integration"))[0]; after != integrateBefore {
		t.Fatalf("workspace prepare must not push the baseline, before=%s after=%s", integrateBefore, after)
	}
	if after := strings.Fields(testGit(t, remote, "rev-parse", "master"))[0]; after != masterBefore {
		t.Fatalf("default branch must stay untouched, before=%s after=%s", masterBefore, after)
	}
}

func TestPrepareRepositoryContinuesWhenDefaultConflictsWithIntegrate(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "shared")
	testGit(t, source, "branch", "agentflow/integration")
	if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte("master\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "master side")
	testGit(t, source, "checkout", "agentflow/integration")
	if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte("integrate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "integrate side")
	if out, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}

	if err := prepareRepository(context.Background(), workspace, domain.RepositoryContext{
		RepositoryID:    "skillhub",
		CloneURL:        remote,
		BaseBranch:      "agentflow/integration",
		IntegrateBranch: "agentflow/integration",
		DefaultBranch:   "master",
		WorkBranch:      "feature/fix-issue-31",
	}); err != nil {
		t.Fatalf("diverged default branch must not fail checkout: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(workspace, "same.txt"))
	if err != nil || string(got) != "integrate\n" {
		t.Fatalf("conflict should abort and keep integrate content, got %q err=%v", got, err)
	}
	remoteIntegrate := strings.Fields(testGit(t, workspace, "ls-remote", "origin", "refs/heads/agentflow/integration"))[0]
	head := testGit(t, workspace, "rev-parse", "HEAD")
	if head != remoteIntegrate {
		t.Fatalf("conflict abort must leave work branch on integrate, remote=%s head=%s", remoteIntegrate, head)
	}
}

func TestAlignIntegrateBaselinePushesCleanMerge(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "shared")
	testGit(t, source, "branch", "agentflow/integration")
	if err := os.WriteFile(filepath.Join(source, "mainline.txt"), []byte("from-master\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "master moved")
	if out, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}
	if err := AlignIntegrateBaseline(domain.RepositoryContext{
		RepositoryID:    "skillhub",
		CloneURL:        remote,
		BaseBranch:      "agentflow/integration",
		IntegrateBranch: "agentflow/integration",
		DefaultBranch:   "master",
	}, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "--git-dir", remote, "cat-file", "-e", "agentflow/integration:mainline.txt").CombinedOutput(); err != nil {
		t.Fatalf("aligned integrate should contain default-branch file: %v", err)
	}
}

func TestAlignIntegrateBaselineReportsConflictWithoutPush(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "shared")
	testGit(t, source, "branch", "agentflow/integration")
	if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte("master\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "master side")
	testGit(t, source, "checkout", "agentflow/integration")
	if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte("integrate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "integrate side")
	if out, err := exec.Command("git", "clone", "--bare", source, remote).CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}
	before := strings.Fields(testGit(t, remote, "rev-parse", "agentflow/integration"))[0]
	err := AlignIntegrateBaseline(domain.RepositoryContext{
		RepositoryID:    "skillhub",
		CloneURL:        remote,
		BaseBranch:      "agentflow/integration",
		IntegrateBranch: "agentflow/integration",
		DefaultBranch:   "master",
	}, "", nil)
	var conflict *AlignConflictError
	if err == nil || !errors.As(err, &conflict) || len(conflict.Files) == 0 {
		t.Fatalf("expected AlignConflictError, got %v", err)
	}
	after := strings.Fields(testGit(t, remote, "rev-parse", "agentflow/integration"))[0]
	if before != after {
		t.Fatalf("conflict must not push integrate: before=%s after=%s", before, after)
	}
}

func TestAlignIntegrateBaselineNoopsWithoutRemote(t *testing.T) {
	if err := AlignIntegrateBaseline(domain.RepositoryContext{
		RepositoryID:    "skillhub",
		BaseBranch:      "agentflow/integration",
		IntegrateBranch: "agentflow/integration",
		DefaultBranch:   "master",
	}, "", nil); err != nil {
		t.Fatalf("empty clone URL must skip align: %v", err)
	}
}

func TestIntegrateScheduledRunDoesNotTouchDefaultBranch(t *testing.T) {
	work, remote := setupIntegrateRepo(t)
	masterBefore := testGit(t, remote, "rev-parse", "refs/heads/master")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	run := domain.Run{
		ID: "run-no-promote", Status: "running", Input: "timeout spike",
		ExecutionContext: domain.TelemetryContext{ScheduleJobID: "job-1", IssueNumber: 32},
		Repository: domain.RepositoryContext{
			GongfengProjectID: "team/skillhub",
			WorkBranch:        "feature/fix-issue-30",
			IntegrateBranch:   "agentflow/integration",
			DefaultBranch:     "master",
		},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	var targets []string
	e.MergeIntoBranch = func(projectID, sourceBranch, targetBranch, title, description string) (string, error) {
		targets = append(targets, targetBranch)
		if targetBranch == "master" || targetBranch == run.Repository.DefaultBranch {
			t.Fatalf("must not merge into default branch: %s → %s", sourceBranch, targetBranch)
		}
		return "!12", nil
	}
	e.runEnvMu.Lock()
	e.runEnvs[run.ID] = &runEnvironment{sandbox: stubWorkspaceSandbox{workspace: work}}
	e.runEnvMu.Unlock()
	rework, err := e.integrateScheduledRun(run.ID)
	if err != nil || rework {
		t.Fatalf("rework=%v err=%v", rework, err)
	}
	if len(targets) != 1 || targets[0] != "agentflow/integration" {
		t.Fatalf("auto-merge targets = %v", targets)
	}
	if got := testGit(t, remote, "rev-parse", "refs/heads/master"); got != masterBefore {
		t.Fatalf("default branch moved: before=%s after=%s", masterBefore, got)
	}
}

func TestPrepareRepositorySeedsMissingIntegrationBranch(t *testing.T) {
	root := t.TempDir()
	source, remote, workspace := filepath.Join(root, "source"), filepath.Join(root, "remote.git"), filepath.Join(root, "workspace")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "base")
	cmd := exec.Command("git", "clone", "--bare", source, remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}
	repo := domain.RepositoryContext{RepositoryID: "repo", CloneURL: remote, DefaultBranch: "master", BaseBranch: "agentflow/integration", IntegrateBranch: "agentflow/integration", WorkBranch: "agentflow/issue-1"}
	if err := prepareRepository(context.Background(), workspace, repo); err != nil {
		t.Fatal(err)
	}
	if got := testGit(t, workspace, "ls-remote", "--heads", "origin", "agentflow/integration"); !strings.Contains(got, "refs/heads/agentflow/integration") {
		t.Fatalf("integration branch was not published: %s", got)
	}
}

func TestIntegrationCommitLabelIncludesIssue(t *testing.T) {
	got := integrationCommitLabel(domain.Run{
		Input:            "sandbox_sessions deadlock\n\nlong body",
		ExecutionContext: domain.TelemetryContext{IssueNumber: 31},
		Repository:       domain.RepositoryContext{WorkBranch: "feature/fix-issue-31-20260903-1030"},
	})
	if got != "issue #31 sandbox_sessions deadlock" {
		t.Fatalf("label = %q", got)
	}
}

type stubWorkspaceSandbox struct{ workspace string }

func (s stubWorkspaceSandbox) ID() string { return "stub" }
func (s stubWorkspaceSandbox) Workspace() string {
	return s.workspace
}
func (s stubWorkspaceSandbox) Exec(context.Context, runtime.CommandSpec, runtime.EventSink) (string, error) {
	return "", nil
}
func (s stubWorkspaceSandbox) Close(context.Context) error { return nil }

func setupIntegrateRepo(t *testing.T) (work, remote string) {
	t.Helper()
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "base")
	testGit(t, source, "branch", "agentflow/integration")
	cmd := exec.Command("git", "clone", "--bare", source, remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create bare remote: %v: %s", err, out)
	}
	work = filepath.Join(root, "work")
	cmd = exec.Command("git", "clone", "--branch", "agentflow/integration", remote, work)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone work: %v: %s", err, out)
	}
	testGit(t, work, "config", "user.email", "test@example.com")
	testGit(t, work, "config", "user.name", "Test")
	testGit(t, work, "checkout", "-b", "feature/fix-issue-30")
	if err := os.WriteFile(filepath.Join(work, "app.go"), []byte("package app\nfunc Fix() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "fix-plan.md"), []byte("# plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, work, "add", ".")
	testGit(t, work, "commit", "-m", "issue work plus sop report")
	return work, remote
}

func TestMergeLatestIntegrationDropsSOPReports(t *testing.T) {
	work, _ := setupIntegrateRepo(t)
	ctx := context.Background()
	repo := domain.RepositoryContext{WorkBranch: "feature/fix-issue-30", IntegrateBranch: "agentflow/integration"}
	if conflicts, err := mergeLatestIntegration(ctx, work, repo, "issue #30 title"); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts=%v err=%v", conflicts, err)
	}
	tracked := testGit(t, work, "ls-files", "--", "fix-plan.md")
	if tracked != "" {
		t.Fatalf("SOP report still tracked: %q", tracked)
	}
	if _, err := os.Stat(filepath.Join(work, "fix-plan.md")); err != nil {
		t.Fatalf("working copy of SOP report should remain: %v", err)
	}
	if !strings.Contains(testGit(t, work, "log", "-1", "--pretty=%s"), "drop SOP deliverables") {
		t.Fatalf("expected drop commit, got %s", testGit(t, work, "log", "-1", "--pretty=%s"))
	}
}

func TestIntegrateScheduledRunUsesMRAndKeepsIntegrateUntouched(t *testing.T) {
	work, remote := setupIntegrateRepo(t)
	before := testGit(t, remote, "rev-parse", "refs/heads/agentflow/integration")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	run := domain.Run{
		ID: "run-mr", Status: "running", Input: "timeout spike",
		ExecutionContext: domain.TelemetryContext{ScheduleJobID: "job-1", IssueNumber: 30},
		Repository: domain.RepositoryContext{
			GongfengProjectID: "team/skillhub",
			WorkBranch:        "feature/fix-issue-30",
			IntegrateBranch:   "agentflow/integration",
			DefaultBranch:     "master",
		},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	var gotProject, gotSrc, gotTgt, gotTitle string
	e.MergeIntoBranch = func(projectID, sourceBranch, targetBranch, title, description string) (string, error) {
		gotProject, gotSrc, gotTgt, gotTitle = projectID, sourceBranch, targetBranch, title
		return "!12", nil
	}
	e.runEnvMu.Lock()
	e.runEnvs[run.ID] = &runEnvironment{sandbox: stubWorkspaceSandbox{workspace: work}}
	e.runEnvMu.Unlock()
	rework, err := e.integrateScheduledRun(run.ID)
	if err != nil || rework {
		t.Fatalf("rework=%v err=%v", rework, err)
	}
	if gotProject != "team/skillhub" || gotSrc != "feature/fix-issue-30" || gotTgt != "agentflow/integration" {
		t.Fatalf("hook args project=%q src=%q tgt=%q", gotProject, gotSrc, gotTgt)
	}
	if !strings.Contains(gotTitle, "issue #30") {
		t.Fatalf("title = %q", gotTitle)
	}
	after := testGit(t, remote, "rev-parse", "refs/heads/agentflow/integration")
	if before != after {
		t.Fatal("integrate branch must not be direct-pushed when MR hook is set")
	}
	if got := testGit(t, remote, "rev-parse", "refs/heads/feature/fix-issue-30"); got == "" {
		t.Fatal("work branch was not pushed")
	}
}

func TestIntegrateMRTitleIncludesIssue(t *testing.T) {
	got := integrateMRTitle(domain.Run{
		Input:            "timeout spike\nbody",
		ExecutionContext: domain.TelemetryContext{IssueNumber: 30},
		Repository:       domain.RepositoryContext{WorkBranch: "feature/fix-issue-30"},
	}, domain.RepositoryContext{}, "feature/fix-issue-30", "agentflow/integration")
	if got != "AgentFlow: issue #30 timeout spike → agentflow/integration" {
		t.Fatalf("title = %q", got)
	}
}

// TestPrepareRepositoriesMultiDirectoryLayout verifies the multi-repository
// workspace layout: every repository is checked out into its own sub-directory
// under the run workspace, all on the same work branch.
func TestPrepareRepositoriesMultiDirectoryLayout(t *testing.T) {
	root := t.TempDir()
	remoteA := setupMultiRepoRemote(t, root, "alpha")
	remoteB := setupMultiRepoRemote(t, root, "beta")
	workspace := filepath.Join(root, "workspace")
	repositories := []domain.RepositoryContext{
		{RepositoryID: "repo-a", Name: "alpha", CloneURL: remoteA, BaseBranch: "master", WorkBranch: "feature/multi"},
		{RepositoryID: "repo-b", Name: "beta", CloneURL: remoteB, BaseBranch: "master", WorkBranch: "feature/multi"},
	}
	domain.AssignRepositoryDirs(repositories)
	if err := prepareRepositories(context.Background(), workspace, repositories); err != nil {
		t.Fatal(err)
	}
	for _, rc := range repositories {
		dir := filepath.Join(workspace, rc.WorkspaceDir)
		if testGit(t, dir, "rev-parse", "--is-inside-work-tree") != "true" {
			t.Fatalf("%s: no git repository at %s", rc.Name, dir)
		}
		if branch := testGit(t, dir, "branch", "--show-current"); branch != "feature/multi" {
			t.Fatalf("%s: branch = %s", rc.Name, branch)
		}
		if _, err := os.Stat(filepath.Join(workspace, ".git")); err == nil {
			t.Fatal("multi-repository layout must not clone into the workspace root")
		}
	}
}

func TestPrepareRepositoriesSingleDirectoryLayout(t *testing.T) {
	root := t.TempDir()
	remote := setupMultiRepoRemote(t, root, "gateway")
	workspace := filepath.Join(root, "workspace")
	repositories := []domain.RepositoryContext{
		{RepositoryID: "repo-a", Name: "gateway", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/single"},
	}
	domain.AssignRepositoryDirs(repositories)
	if repositories[0].WorkspaceDir != "gateway" {
		t.Fatalf("WorkspaceDir = %q, want gateway", repositories[0].WorkspaceDir)
	}
	if err := prepareRepositories(context.Background(), workspace, repositories); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(workspace, repositories[0].WorkspaceDir)
	if testGit(t, dir, "rev-parse", "--is-inside-work-tree") != "true" {
		t.Fatalf("no git repository at %s", dir)
	}
	if branch := testGit(t, dir, "branch", "--show-current"); branch != "feature/single" {
		t.Fatalf("branch = %s", branch)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err == nil {
		t.Fatal("single-repository layout must not clone into the workspace root")
	}
}

func setupMultiRepoRemote(t *testing.T, root, name string) string {
	t.Helper()
	source := filepath.Join(root, name+"-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "master")
	testGit(t, source, "config", "user.email", "test@example.com")
	testGit(t, source, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(source, name+".txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "base")
	testGit(t, source, "branch", "agentflow/integration")
	remote := filepath.Join(root, name+"-remote.git")
	cmd := exec.Command("git", "clone", "--bare", source, remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create bare remote %s: %v: %s", name, err, out)
	}
	return remote
}

// TestIntegrateScheduledRunMultiRepository verifies a scheduled run bound to
// two repositories integrates both equally: each repository gets its own work
// branch push and its own baseline MR, keyed by its own Gongfeng project.
func TestIntegrateScheduledRunMultiRepository(t *testing.T) {
	root := t.TempDir()
	remoteA := setupMultiRepoRemote(t, root, "frontend")
	remoteB := setupMultiRepoRemote(t, root, "backend")
	workspace := filepath.Join(root, "workspace")
	checkout := func(remote, dir string) {
		cmd := exec.Command("git", "clone", "--branch", "agentflow/integration", remote, dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clone %s: %v: %s", dir, err, out)
		}
		testGit(t, dir, "config", "user.email", "test@example.com")
		testGit(t, dir, "config", "user.name", "Test")
	}
	dirA := filepath.Join(workspace, "frontend")
	dirB := filepath.Join(workspace, "backend")
	checkout(remoteA, dirA)
	checkout(remoteB, dirB)
	testGit(t, dirA, "checkout", "-b", "feature/multi-1")
	testGit(t, dirB, "checkout", "-b", "feature/multi-1")
	if err := os.WriteFile(filepath.Join(dirA, "frontend.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "backend.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	run := domain.Run{
		ID: "run-multi-repo", Status: "running", Input: "multi repo change",
		ExecutionContext: domain.TelemetryContext{ScheduleJobID: "job-9", IssueNumber: 9},
		Repository: domain.RepositoryContext{
			RepositoryID: "repo-a", GongfengProjectID: "team/frontend",
			WorkBranch: "feature/multi-1", IntegrateBranch: "agentflow/integration",
		},
		Repositories: []domain.RepositoryContext{
			{
				RepositoryID: "repo-a", GongfengProjectID: "team/frontend",
				WorkBranch: "feature/multi-1", IntegrateBranch: "agentflow/integration",
				WorkspaceDir: "frontend",
			},
			{
				RepositoryID: "repo-b", GongfengProjectID: "team/backend",
				WorkBranch: "feature/multi-1", IntegrateBranch: "agentflow/integration",
				WorkspaceDir: "backend",
			},
		},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	remotes := map[string]string{"team/frontend": remoteA, "team/backend": remoteB}
	type mergeCall struct{ project, source, target string }
	var calls []mergeCall
	e.MergeIntoBranch = func(projectID, sourceBranch, targetBranch, title, description string) (string, error) {
		calls = append(calls, mergeCall{projectID, sourceBranch, targetBranch})
		return "!9", nil
	}
	e.runEnvMu.Lock()
	e.runEnvs[run.ID] = &runEnvironment{sandbox: stubWorkspaceSandbox{workspace: workspace}}
	e.runEnvMu.Unlock()
	rework, err := e.integrateScheduledRun(run.ID)
	if err != nil || rework {
		t.Fatalf("rework=%v err=%v", rework, err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected one MR per repository, got %v", calls)
	}
	seen := map[string]bool{}
	for _, c := range calls {
		seen[c.project] = true
		if c.source != "feature/multi-1" || c.target != "agentflow/integration" {
			t.Fatalf("unexpected MR %v", c)
		}
		if testGit(t, remotes[c.project], "rev-parse", "refs/heads/feature/multi-1") == "" {
			t.Fatalf("work branch not pushed for %s", c.project)
		}
	}
	if !seen["team/frontend"] || !seen["team/backend"] {
		t.Fatalf("both repositories must integrate: %v", calls)
	}
}

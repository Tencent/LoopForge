package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func bareRemoteWithBaseline(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	bare := filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, source, "init", "-q", "-b", "master")
	runGitT(t, source, "-c", "user.name=t", "-c", "user.email=t@t", "add", "README.md")
	runGitT(t, source, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "baseline")
	if out, err := exec.Command("git", "clone", "-q", "--bare", source, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone bare: %v: %s", err, out)
	}
	return bare
}

// 执行机器上跑完的节点不提交、不推送：改动留在本机工作区给下个节点用，远程工作分支只由交付步骤写入。
func TestRunnerModeNodeKeepsChangesLocal(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	remote := bareRemoteWithBaseline(t)
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		Name:                 "work-branch-runner",
		MaxConcurrency:       1,
		HeartbeatInterval:    time.Second,
		LongPollTimeout:      2 * time.Second,
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = daemon.Run(ctx) }()
	waitFor(t, 5*time.Second, "runner registration", func() bool { return len(s.Runners()) == 1 })
	runner := s.Runners()[0]
	runner.OwnerUserID = 42
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultRunner(42, runner.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "runner-work-branch", Name: "Runner work branch", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "implement", Name: "实现", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "printf 'fixed\\n' > feature.txt && mkdir -p node_modules/x && printf 'dep\\n' > node_modules/x/index.js", MaxAttempts: 1},
			{ID: "verify", Name: "验证", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "test \"$(cat feature.txt)\" = fixed && test -n \"$(git status --porcelain -- feature.txt)\" && echo uncommitted", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "implement-verify", Source: "implement", Target: "verify", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{RepositoryID: "repo", Name: "svc", CloneURL: remote, BaseBranch: "master", WorkBranch: "fix/runner-push"}, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "runner work-branch workflow", func() bool {
		current, ok := s.Run(run.ID)
		return ok && (current.Status == "succeeded" || current.Status == "failed")
	})
	final, _ := s.Run(run.ID)
	if final.Status != "succeeded" {
		t.Fatalf("status = %s, implement error = %q, verify error = %q", final.Status, final.Nodes["implement"].Error, final.Nodes["verify"].Error)
	}
	if refs := runGitT(t, remote, "for-each-ref", "refs/heads/fix/runner-push"); refs != "" {
		t.Fatalf("a node pushed the work branch: %s", refs)
	}
	for _, task := range s.RunnerTasks() {
		if task.RunID == run.ID && (task.Action == domain.RunnerTaskActionPushWorkBranch || task.Action == domain.RunnerTaskActionPullWorkBranch) {
			t.Fatalf("work-branch sync task queued: %s %s", task.ID, task.Action)
		}
	}
}

// 执行机器克隆仓库后，工作区的 .git/config 里不能留下用户的 git 令牌。
func TestCheckoutRepositoryKeepsTokenOutOfGitConfig(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	workspace := t.TempDir()
	repo := &domain.RunnerRepository{CloneURL: remote, Ref: "master", GitToken: "tok-secret-123"}
	if err := checkoutRepository(context.Background(), workspace, repo); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	config, err := os.ReadFile(filepath.Join(workspace, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "tok-secret-123") {
		t.Fatalf(".git/config leaks the token:\n%s", config)
	}
	if got := runGitT(t, workspace, "remote", "get-url", "origin"); got != remote {
		t.Fatalf("origin = %q, want %q", got, remote)
	}
}

func TestInvocationGitTokenPrefersRepositories(t *testing.T) {
	inv := &domain.RunnerInvocation{
		Repository:   &domain.RunnerRepository{GitToken: "legacy"},
		Repositories: []domain.RunnerRepository{{}, {GitToken: " multi "}},
	}
	if got := invocationGitToken(inv); got != "multi" {
		t.Fatalf("token = %q", got)
	}
	if got := invocationGitToken(&domain.RunnerInvocation{Repository: &domain.RunnerRepository{GitToken: "legacy"}}); got != "legacy" {
		t.Fatalf("legacy token = %q", got)
	}
	if got := invocationGitToken(nil); got != "" {
		t.Fatalf("nil token = %q", got)
	}
}

func TestCheckoutRepositoryContinuesExistingRemoteBranch(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	seed := filepath.Join(t.TempDir(), "seed")
	runGitT(t, filepath.Dir(seed), "clone", "-q", remote, seed)
	runGitT(t, seed, "checkout", "-q", "-b", "feature/existing")
	if err := os.WriteFile(filepath.Join(seed, "existing.txt"), []byte("existing development\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitT(t, seed, "add", "existing.txt")
	runGitT(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "existing development")
	runGitT(t, seed, "push", "-q", "origin", "feature/existing")
	workspace := t.TempDir()
	repository := &domain.RunnerRepository{CloneURL: remote, Ref: "master", WorkBranch: "feature/existing", RequireExistingWorkBranch: true}
	if err := checkoutRepository(context.Background(), workspace, repository); err != nil {
		t.Fatal(err)
	}
	if current := runGitT(t, workspace, "branch", "--show-current"); current != "feature/existing" {
		t.Fatalf("checked out %q, want existing branch", current)
	}
	if _, err := os.Stat(filepath.Join(workspace, "existing.txt")); err != nil {
		t.Fatal("checkout used master instead of the existing branch")
	}
	if err := os.WriteFile(filepath.Join(workspace, "local.txt"), []byte("unsaved local work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkoutRepository(context.Background(), workspace, repository); err != nil {
		t.Fatalf("reuse existing checkout: %v", err)
	}
	runGitT(t, remote, "update-ref", "-d", "refs/heads/feature/existing")
	if err := checkoutRepository(context.Background(), workspace, repository); err == nil {
		t.Fatal("reused checkout accepted a deleted remote head")
	}
	if content, err := os.ReadFile(filepath.Join(workspace, "local.txt")); err != nil || string(content) != "unsaved local work\n" {
		t.Fatalf("failed validation lost local work: %q %v", content, err)
	}
}

func TestCheckoutRepositoryStrictMissingOrTagNeverCreatesBranch(t *testing.T) {
	for _, tagOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "tag only"}[tagOnly], func(t *testing.T) {
			remote := bareRemoteWithBaseline(t)
			if tagOnly {
				runGitT(t, remote, "tag", "feature/missing", "master")
			}
			workspace := t.TempDir()
			repository := &domain.RunnerRepository{CloneURL: remote, Ref: "master", WorkBranch: "feature/missing", RequireExistingWorkBranch: true}
			if err := checkoutRepository(context.Background(), workspace, repository); err == nil || !strings.Contains(err.Error(), "已有远端分支") {
				t.Fatalf("expected missing remote head error, got %v", err)
			}
			if out := runGitT(t, workspace, "for-each-ref", "--format=%(refname)", "refs/heads/feature/missing"); out != "" {
				t.Fatalf("strict checkout created a local fallback branch: %s", out)
			}
		})
	}
}

func TestSyncTaskExistingWorkBranchRejectsDeletedRemote(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	runGitT(t, remote, "branch", "feature/existing", "master")
	workspace := t.TempDir()
	repository := domain.RunnerRepository{CloneURL: remote, Ref: "master", WorkBranch: "feature/existing", RequireExistingWorkBranch: true, SyncWorkBranch: true}
	if err := checkoutRepository(context.Background(), workspace, &repository); err != nil {
		t.Fatal(err)
	}
	runGitT(t, remote, "update-ref", "-d", "refs/heads/feature/existing")
	invocation := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{repository}}
	for _, push := range []bool{false, true} {
		if _, err := syncTaskWorkBranches(context.Background(), workspace, invocation, push, "continue work"); err == nil {
			t.Fatalf("strict sync push=%v accepted a deleted remote branch", push)
		}
	}
	if got := runGitT(t, remote, "for-each-ref", "--format=%(refname)", "refs/heads/feature/existing"); got != "" {
		t.Fatal("strict runner sync recreated a deleted remote branch")
	}
}

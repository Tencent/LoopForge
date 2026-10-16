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

// 执行机器上跑完的节点，改动要推到远程工作分支，控制面和其他机器才能接着用。
func TestRunnerModeNodePushesWorkBranch(t *testing.T) {
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
	if !runner.SupportsWorkBranchSync() {
		t.Fatalf("runner version %q does not advertise work-branch sync", runner.RunnerVersion)
	}
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
			{ID: "verify", Name: "验证", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "test \"$(git status --porcelain -- feature.txt)\" = '' && echo committed", MaxAttempts: 1},
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
	head := runGitT(t, remote, "rev-parse", "refs/heads/fix/runner-push")
	files := runGitT(t, remote, "show", "--name-only", "--format=%s", head)
	if !strings.Contains(files, "feature.txt") || !strings.Contains(files, "agentflow: 实现 ("+run.ID+")") {
		t.Fatalf("remote work branch misses the node commit: %s", files)
	}
	if strings.Contains(files, "node_modules") {
		t.Fatalf("dependency tree was pushed: %s", files)
	}
	var pushTasks int
	for _, task := range s.RunnerTasks() {
		if task.RunID == run.ID && task.Action == domain.RunnerTaskActionPushWorkBranch {
			pushTasks++
			if task.Status != domain.RunnerTaskSuccess {
				t.Fatalf("push task %s ended %s: %s", task.ID, task.Status, task.Error)
			}
		}
	}
	if pushTasks != 2 {
		t.Fatalf("push tasks = %d, want one per succeeded node", pushTasks)
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

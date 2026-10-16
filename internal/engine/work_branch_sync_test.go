package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	flowruntime "agentflow/internal/runtime"
)

func checkoutForSync(t *testing.T, root, name string) (string, domain.RepositoryContext) {
	t.Helper()
	remote := setupMultiRepoRemote(t, root, "svc")
	rc := domain.RepositoryContext{RepositoryID: "repo-svc", Name: "svc", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/fix-1"}
	dir := filepath.Join(root, name)
	if err := prepareRepository(context.Background(), dir, rc); err != nil {
		t.Fatal(err)
	}
	return dir, rc
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 节点成功后改动要推到远程工作分支，另一台机器重新克隆就能接着用。
func TestSyncWorkBranchPushesNodeChangesForAFreshClone(t *testing.T) {
	root := t.TempDir()
	dir, rc := checkoutForSync(t, root, "host-a")
	writeFile(t, filepath.Join(dir, "svc.txt"), "fixed\n")
	writeFile(t, filepath.Join(dir, "scripts", "e2e.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(dir, ".agentflow", "inputs", "x.md"), "control\n")
	writeFile(t, filepath.Join(dir, "config", "mcporter.json"), "{\"token\":\"secret\"}\n")
	writeFile(t, filepath.Join(dir, "web", ".npmrc"), "//registry/:_authToken=secret\n")
	writeFile(t, filepath.Join(dir, ".env.local"), "TOKEN=secret\n")
	writeFile(t, filepath.Join(dir, ".env.example"), "TOKEN=\n")

	head, err := syncWorkBranch(context.Background(), dir, rc, "agentflow: 修复 (run-1)")
	if err != nil {
		t.Fatal(err)
	}
	if head == "" || testGit(t, rc.CloneURL, "rev-parse", "refs/heads/feature/fix-1") != head {
		t.Fatalf("work branch not pushed: head=%s", head)
	}
	files := testGit(t, dir, "show", "--name-only", "--format=", "HEAD")
	for _, want := range []string{"svc.txt", "scripts/e2e.sh", ".env.example"} {
		if !strings.Contains(files, want) {
			t.Fatalf("commit misses %s: %s", want, files)
		}
	}
	for _, leaked := range []string{".agentflow", "mcporter.json", ".npmrc", ".env.local"} {
		if strings.Contains(files, leaked) {
			t.Fatalf("commit leaked %s: %s", leaked, files)
		}
	}

	again, err := syncWorkBranch(context.Background(), dir, rc, "agentflow: 评审 (run-1)")
	if err != nil || again != "" {
		t.Fatalf("clean node must not push: head=%q err=%v", again, err)
	}

	other := filepath.Join(root, "host-b")
	if err := prepareRepository(context.Background(), other, rc); err != nil {
		t.Fatal(err)
	}
	if got := testGit(t, other, "rev-parse", "HEAD"); got != head {
		t.Fatalf("fresh clone HEAD = %s, want %s", got, head)
	}
	if _, err := os.Stat(filepath.Join(other, "scripts", "e2e.sh")); err != nil {
		t.Fatalf("fresh clone misses upstream file: %v", err)
	}
}

func TestSyncWorkBranchNeverPushesSharedBranches(t *testing.T) {
	root := t.TempDir()
	dir, rc := checkoutForSync(t, root, "host-a")
	for _, shared := range []domain.RepositoryContext{
		{RepositoryID: rc.RepositoryID, WorkBranch: "master", BaseBranch: "master"},
		{RepositoryID: rc.RepositoryID, WorkBranch: "agentflow/integration", BaseBranch: "master", IntegrateBranch: "agentflow/integration"},
		{RepositoryID: rc.RepositoryID, WorkBranch: "feature/x", BaseBranch: "master", AlignOnly: true},
		{RepositoryID: rc.RepositoryID, BaseBranch: "master"},
	} {
		if workBranchSyncable(shared) {
			t.Fatalf("branch must not be synced: %+v", shared)
		}
	}
	writeFile(t, filepath.Join(dir, "svc.txt"), "changed\n")
	rc.WorkBranch = "master"
	if head, err := syncWorkBranch(context.Background(), dir, rc, "x"); err != nil || head != "" {
		t.Fatalf("shared branch synced: head=%q err=%v", head, err)
	}
}

// 远程分支被别人推过时不能强推覆盖，节点要失败并说明原因。
func TestSyncWorkBranchRefusesToOverwriteDivergedRemote(t *testing.T) {
	root := t.TempDir()
	dir, rc := checkoutForSync(t, root, "host-a")
	writeFile(t, filepath.Join(dir, "svc.txt"), "first\n")
	if _, err := syncWorkBranch(context.Background(), dir, rc, "first"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "manual")
	if err := prepareRepository(context.Background(), other, rc); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(other, "manual.txt"), "manual\n")
	testGit(t, other, "add", "manual.txt")
	testGit(t, other, "-c", "user.name=m", "-c", "user.email=m@m", "commit", "-m", "manual")
	testGit(t, other, "push", "origin", "HEAD:refs/heads/feature/fix-1")
	remoteHead := testGit(t, rc.CloneURL, "rev-parse", "refs/heads/feature/fix-1")

	writeFile(t, filepath.Join(dir, "svc.txt"), "second\n")
	_, err := syncWorkBranch(context.Background(), dir, rc, "second")
	if err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("diverged push error = %v", err)
	}
	if got := testGit(t, rc.CloneURL, "rev-parse", "refs/heads/feature/fix-1"); got != remoteHead {
		t.Fatalf("remote was overwritten: %s", got)
	}
}

// 子 SOP 在自己的工作区推了同一工作分支，父流程下个节点要先快进到远程再继续。
func TestPullWorkBranchFastForwardsAfterChildRunPushed(t *testing.T) {
	root := t.TempDir()
	parent, rc := checkoutForSync(t, root, "parent")
	writeFile(t, filepath.Join(parent, "pre.txt"), "pre\n")
	if _, err := syncWorkBranch(context.Background(), parent, rc, "pre"); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	if err := prepareRepository(context.Background(), child, rc); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(child, "child.txt"), "child\n")
	childHead, err := syncWorkBranch(context.Background(), child, rc, "child")
	if err != nil || childHead == "" {
		t.Fatalf("child push: head=%q err=%v", childHead, err)
	}

	got, err := pullWorkBranch(context.Background(), parent, rc)
	if err != nil || got != childHead {
		t.Fatalf("pull = %q, %v; want %s", got, err, childHead)
	}
	if _, err := os.Stat(filepath.Join(parent, "child.txt")); err != nil {
		t.Fatalf("parent misses child change: %v", err)
	}
	if again, err := pullWorkBranch(context.Background(), parent, rc); err != nil || again != "" {
		t.Fatalf("current checkout must not move: %q %v", again, err)
	}
	writeFile(t, filepath.Join(parent, "after.txt"), "after\n")
	if _, err := syncWorkBranch(context.Background(), parent, rc, "after"); err != nil {
		t.Fatalf("parent push after pull: %v", err)
	}

	writeFile(t, filepath.Join(parent, "local.txt"), "local\n")
	testGit(t, parent, "add", "local.txt")
	testGit(t, parent, "-c", "user.name=m", "-c", "user.email=m@m", "commit", "-m", "local only")
	if got, err := pullWorkBranch(context.Background(), parent, rc); err != nil || got != "" {
		t.Fatalf("local-ahead checkout must stay: %q %v", got, err)
	}
	testGit(t, child, "fetch", "origin", "+refs/heads/feature/fix-1:refs/remotes/origin/feature/fix-1")
	testGit(t, child, "reset", "--hard", "origin/feature/fix-1")
	writeFile(t, filepath.Join(child, "other.txt"), "other\n")
	if _, err := syncWorkBranch(context.Background(), child, rc, "other"); err != nil {
		t.Fatal(err)
	}
	rebased, err := pullWorkBranch(context.Background(), parent, rc)
	if err != nil || rebased == "" {
		t.Fatalf("diverged pull must rebase local commits: %q %v", rebased, err)
	}
	for _, f := range []string{"local.txt", "other.txt"} {
		if _, err := os.Stat(filepath.Join(parent, f)); err != nil {
			t.Fatalf("rebased checkout misses %s: %v", f, err)
		}
	}
	if _, err := syncWorkBranch(context.Background(), parent, rc, "push rebased"); err != nil {
		t.Fatalf("push after rebase: %v", err)
	}

	writeFile(t, filepath.Join(parent, "svc.txt"), "parent\n")
	testGit(t, parent, "add", "svc.txt")
	testGit(t, parent, "-c", "user.name=m", "-c", "user.email=m@m", "commit", "-m", "parent edit")
	testGit(t, child, "fetch", "origin", "+refs/heads/feature/fix-1:refs/remotes/origin/feature/fix-1")
	testGit(t, child, "reset", "--hard", "origin/feature/fix-1")
	writeFile(t, filepath.Join(child, "svc.txt"), "child\n")
	if _, err := syncWorkBranch(context.Background(), child, rc, "child edit"); err != nil {
		t.Fatal(err)
	}
	before := testGit(t, parent, "rev-parse", "HEAD")
	if _, err := pullWorkBranch(context.Background(), parent, rc); err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("conflicting pull error = %v", err)
	}
	if got := testGit(t, parent, "rev-parse", "HEAD"); got != before {
		t.Fatalf("failed rebase moved HEAD: %s", got)
	}
	if _, err := os.Stat(filepath.Join(parent, ".git", "rebase-merge")); err == nil {
		t.Fatal("rebase left in progress")
	}
}

func TestPullWorkBranchIgnoresUnpushedBranch(t *testing.T) {
	root := t.TempDir()
	dir, rc := checkoutForSync(t, root, "host-a")
	if got, err := pullWorkBranch(context.Background(), dir, rc); err != nil || got != "" {
		t.Fatalf("missing remote branch: %q %v", got, err)
	}
}

// 代码没推到工作分支的 run（分支就是 master、AlignOnly 等）不能换机器重建，否则前面节点的改动会悄悄丢失。
func TestDeadBoundHostWithUnpushedBranchStaysBlocked(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	worker := leaseEngine(t, s, "worker-b")
	worker.ConfigureExecution("worker-b", "worker-b-host", 30*time.Second, true)
	run := domain.Run{
		ID: "run-dead-master", Status: "failed", WorkspaceHost: "dead-host", WorkspaceRoot: "/gone",
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud},
		Repository:       domain.RepositoryContext{RepositoryID: "repo-1", BaseBranch: "master", WorkBranch: "master"},
	}
	if !worker.workspaceBlocked(run) {
		t.Fatal("run on a shared branch must not be rebuilt on another host")
	}
	run.Repository.WorkBranch = "feature/x"
	if worker.workspaceBlocked(run) {
		t.Fatal("run on a pushed work branch should be taken over")
	}
}

// 绑定 host 已经没有存活 worker 时，从节点重跑由当前 worker 接管并重建工作区。
func TestRetryOnDeadBoundHostIsTakenOverByLiveWorker(t *testing.T) {
	s, _ := repo.OpenTest()
	defer s.Close()
	marker := filepath.Join(t.TempDir(), "ready")
	wf := domain.WorkflowDefinition{ID: "retry-takeover", Name: "retry", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "work", Name: "work", Type: "command", Command: "test -e " + marker, MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	api := leaseEngine(t, s, "api")
	api.ConfigureExecution("api", "api-host", 30*time.Second, false)
	workerA := leaseEngine(t, s, "worker-a")
	workerA.ConfigureExecution("worker-a", "worker-a-host", 30*time.Second, true)
	workerB := leaseEngine(t, s, "worker-b")
	workerB.ConfigureExecution("worker-b", "worker-b-host", 30*time.Second, true)
	heartbeatHost(t, s, "worker-b", "worker-b-host")

	run, err := api.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if claimed := workerA.ResumeInterruptedRuns(); len(claimed) != 1 {
		t.Fatalf("worker-a claimed %v", claimed)
	}
	waitRun(t, s, run.ID, 2*time.Second, func(got domain.Run) bool {
		return got.Status == "failed" && got.WorkspaceHost == "worker-a-host"
	})
	// worker-a-host never heartbeats: its workspace is gone with it.
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := api.Retry(run.ID, "work", "从指定节点重跑", ""); err != nil {
		t.Fatal(err)
	}
	if claimed := workerB.ResumeInterruptedRuns(); len(claimed) != 1 || claimed[0] != run.ID {
		t.Fatalf("worker-b did not take over the dead bind: %v", claimed)
	}
	got := waitRun(t, s, run.ID, 3*time.Second, func(got domain.Run) bool {
		return got.Status == "succeeded"
	})
	if got.WorkspaceHost != "worker-b-host" || got.WorkspaceRoot != workerB.sandboxes.Root() {
		t.Fatalf("bind not moved: host=%s root=%s", got.WorkspaceHost, got.WorkspaceRoot)
	}
	rebound := false
	for _, ev := range got.Events {
		if ev.Type == "workspace.rebound" {
			rebound = true
		}
	}
	if !rebound {
		t.Fatal("missing workspace.rebound event")
	}
}

func runnerSyncFixture(t *testing.T, version string) (*repo.Store, *Engine, domain.Run) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := New(s, flowruntime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	e.ConfigureExecution("worker-1", "worker-host", 30*time.Second, true)
	if err := s.PutRunner(domain.Runner{ID: "runner-1", Status: domain.RunnerStatusOnline, RunnerVersion: version}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-runner-sync", Status: "running", OwnerInstanceID: "worker-1", Generation: 1, LeaseUntil: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
		ExecutionContext: domain.TelemetryContext{RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared},
		Nodes:            map[string]*domain.NodeRun{"dev": {NodeID: "dev", Status: "running"}},
		Repositories: []domain.RepositoryContext{
			{RepositoryID: "repo-svc", Name: "svc", CloneURL: "https://git.example.com/a/svc.git", BaseBranch: "master", WorkBranch: "feature/fix-1", WorkspaceDir: "svc"},
			{RepositoryID: "repo-lib", Name: "lib", CloneURL: "https://git.example.com/a/lib.git", BaseBranch: "master", WorkBranch: "master", WorkspaceDir: "lib"},
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return s, e, run
}

// 执行机器上的 run 也要在节点成功后推工作分支；推哪个仓库由控制面按分支策略决定。
func TestRunnerWorkBranchSyncQueuesPushToCurrentRunner(t *testing.T) {
	s, e, run := runnerSyncFixture(t, domain.RunnerVersionWorkBranchSync)
	ctx, cancel := context.WithTimeout(withGitToken(context.Background(), "tok_user"), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- e.runnerWorkBranchSync(ctx, "runner-1", run, domain.WorkflowNode{ID: "dev", Name: "开发"}, domain.RunnerTaskActionPushWorkBranch, "agentflow: 开发 (run-runner-sync)")
	}()
	var task domain.RunnerTask
	for task.ID == "" {
		select {
		case <-ctx.Done():
			t.Fatal("push task was not queued")
		case <-time.After(20 * time.Millisecond):
		}
		tasks, _ := s.ActiveRunnerTasksForRun(run.ID)
		for _, candidate := range tasks {
			if candidate.Action == domain.RunnerTaskActionPushWorkBranch {
				task = candidate
			}
		}
	}
	if task.RunnerID != "runner-1" || task.Workspace != "runs/run-runner-sync/workspace" || task.WorkBranchMessage != "agentflow: 开发 (run-runner-sync)" || task.Invocation == nil {
		t.Fatalf("unexpected push task: %+v", task)
	}
	flags := map[string]bool{}
	for _, repository := range task.Invocation.Repositories {
		flags[repository.Name] = repository.SyncWorkBranch
		if repository.GitToken != "tok_user" {
			t.Fatalf("repository %s misses the responsible user's token", repository.Name)
		}
	}
	if !flags["svc"] || flags["lib"] {
		t.Fatalf("sync policy = %v, want only the feature branch", flags)
	}
	task.Status = domain.RunnerTaskSuccess
	task.Result = `[{"repositoryId":"repo-svc","name":"svc","workBranch":"feature/fix-1","head":"abc123"}]`
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	e.flushAllObservations()
	got, _ := s.Run(run.ID)
	pushed := false
	for _, event := range got.Telemetry {
		if event.Name == "work_branch.pushed" && event.Attributes["commit"] == "abc123" {
			pushed = true
		}
	}
	if !pushed {
		t.Fatalf("missing work_branch.pushed telemetry: %+v", got.Telemetry)
	}
}

func TestRunnerWorkBranchSyncFailsNodeWhenRunnerPushFails(t *testing.T) {
	s, e, run := runnerSyncFixture(t, domain.RunnerVersionWorkBranchSync)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- e.runnerWorkBranchSync(ctx, "runner-1", run, domain.WorkflowNode{ID: "dev"}, domain.RunnerTaskActionPushWorkBranch, "m")
	}()
	for {
		tasks, _ := s.ActiveRunnerTasksForRun(run.ID)
		if len(tasks) == 1 {
			task := tasks[0]
			task.Status, task.Error = domain.RunnerTaskFailed, "svc: 远程工作分支 feature/fix-1 有本机没有的提交，拒绝覆盖"
			if err := s.PutRunnerTask(task); err != nil {
				t.Fatal(err)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("push task was not queued")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("push failure = %v", err)
	}
}

// 旧版执行机器不认识同步动作，会把它当成 Agent 调用执行，所以不能下发。
func TestRunnerWorkBranchSyncSkipsOutdatedRunner(t *testing.T) {
	s, e, run := runnerSyncFixture(t, "0.1.0")
	if err := e.runnerWorkBranchSync(context.Background(), "runner-1", run, domain.WorkflowNode{ID: "dev"}, domain.RunnerTaskActionPushWorkBranch, "m"); err != nil {
		t.Fatal(err)
	}
	if tasks, _ := s.ActiveRunnerTasksForRun(run.ID); len(tasks) != 0 {
		t.Fatalf("outdated runner received %d tasks", len(tasks))
	}
	e.flushAllObservations()
	got, _ := s.Run(run.ID)
	skipped := false
	for _, event := range got.Telemetry {
		skipped = skipped || event.Name == "work_branch.skipped"
	}
	if !skipped {
		t.Fatal("missing work_branch.skipped telemetry")
	}
}

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

// 子 SOP 直接在父流程的工作区里干活：父流程前面节点的改动子流程看得到，子流程的改动父流程后面节点也看得到，不经过远端分支。
func TestSOPChildWorksInParentWorkspace(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	child := domain.WorkflowDefinition{
		ID: "child-edit", Name: "Child", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{
			ID: "edit", Name: "Edit", Type: "script", ScriptLanguage: "shell",
			Script: "test -f pre.txt && printf child > child.txt", MaxAttempts: 1,
		}},
	}
	parent := domain.WorkflowDefinition{
		ID: "parent-edit", Name: "Parent", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "pre", Name: "Pre", Type: "script", ScriptLanguage: "shell", Script: "printf pre > pre.txt", MaxAttempts: 1},
			{ID: "sub", Name: "Sub", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1},
			{ID: "use", Name: "Use", Type: "script", ScriptLanguage: "shell", Script: "test -f child.txt", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "e1", Source: "pre", Target: "sub", When: "success"},
			{ID: "e2", Source: "sub", Target: "use", When: "success"},
		},
	}
	for _, wf := range []domain.WorkflowDefinition{child, parent} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	e := New(s, flowruntime.NewManagerWithLimit(root, 2))
	run, err := e.Start(parent.ID, "x")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	finished, _ := s.Run(run.ID)
	childRun, ok := s.Run(finished.Nodes["sub"].ChildRunID)
	if !ok || childRun.Status != "succeeded" {
		t.Fatalf("child run = %+v", childRun.Status)
	}
	if got := workspaceRunID(childRun); got != run.ID {
		t.Fatalf("child workspace run = %q, want %q", got, run.ID)
	}
	if _, err := os.Stat(filepath.Join(root, childRun.ID)); !os.IsNotExist(err) {
		t.Fatalf("child must not get a workspace of its own: %v", err)
	}
}

// 工作区所在机器已下线、且工作区里有代码改动：不能换机器接着跑（改动没推送，已经丢了），要失败并提示重跑。
func TestDeadBoundHostWithRepositoryIsLost(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	worker := leaseEngine(t, s, "worker-b")
	worker.ConfigureExecution("worker-b", "worker-b-host", 30*time.Second, true)
	run := domain.Run{
		ID: "run-dead", Status: "failed", WorkspaceHost: "dead-host", WorkspaceRoot: "/gone",
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud},
		Repository:       domain.RepositoryContext{RepositoryID: "repo-1", BaseBranch: "master", WorkBranch: "feature/x"},
	}
	if worker.workspaceBlocked(run) {
		t.Fatal("a bind to a dead host must not block forever")
	}
	if !worker.workspaceLost(run) {
		t.Fatal("code changes on a dead host are lost")
	}
	if msg := worker.workspaceLostMessage(run); !strings.Contains(msg, "重跑") || orphanSettleStatus(msg) != "failed" {
		t.Fatalf("lost message = %q", msg)
	}
	run.Repository = domain.RepositoryContext{}
	if worker.workspaceLost(run) {
		t.Fatal("a run without code may be rebuilt elsewhere")
	}
}

// 没有代码仓库的 run：绑定 host 已经没有存活 worker 时，从节点重跑由当前 worker 接管并重建工作区。
func TestRetryOnDeadBoundHostIsTakenOverByLiveWorker(t *testing.T) {
	t.Parallel()
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
}

// 子 SOP 跟着父流程的工作区绑定走：父流程绑在已下线的机器上，子流程也算丢失。
func TestChildFollowsParentWorkspaceBind(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	worker := leaseEngine(t, s, "worker-b")
	worker.ConfigureExecution("worker-b", "worker-b-host", 30*time.Second, true)
	parent := domain.Run{ID: "run-parent", Status: "running", WorkspaceHost: "dead-host", WorkspaceRoot: "/gone", Nodes: map[string]*domain.NodeRun{}}
	if err := s.PutRun(parent); err != nil {
		t.Fatal(err)
	}
	child := domain.Run{
		ID: "run-child", Status: "running", WorkspaceHost: "worker-b-host", WorkspaceRoot: worker.sandboxes.Root(),
		ExecutionContext: domain.TelemetryContext{ParentRunID: parent.ID, WorkspaceRunID: parent.ID},
	}
	if !worker.foreignWorkspaceBind(child) || !worker.workspaceLost(child) {
		t.Fatal("child must follow the parent's dead bind")
	}
	if err := worker.adoptForeignWorkspace(child); err == nil || !strings.Contains(err.Error(), "父流程") {
		t.Fatalf("adopt child = %v", err)
	}
}

// 中断的 run 在已下线机器上有代码改动：恢复时直接失败，并把绑定挪到当前机器，用户重跑时才能从头拉代码。
func TestResumeFailsRunWhoseWorkspaceWasLost(t *testing.T) {
	t.Parallel()
	s, _ := repo.OpenTest()
	defer s.Close()
	worker := leaseEngine(t, s, "worker-b")
	worker.ConfigureExecution("worker-b", "worker-b-host", 30*time.Second, true)
	heartbeatHost(t, s, "worker-b", "worker-b-host")
	now := time.Now().UTC()
	run := domain.Run{
		ID: "run-lost", WorkflowID: "wf", Status: "running", WorkspaceHost: "dead-host", WorkspaceRoot: "/gone",
		OwnerInstanceID: "worker-a", Generation: 1, LeaseUntil: now.Add(-time.Minute), CreatedAt: now, UpdatedAt: now,
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Nodes: []domain.WorkflowNode{{ID: "fix", Type: "agent"}}},
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud},
		Repository:       domain.RepositoryContext{RepositoryID: "repo-1", BaseBranch: "master", WorkBranch: "feature/x"},
		Nodes:            map[string]*domain.NodeRun{"fix": {NodeID: "fix", Status: "running"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if resumed := worker.ResumeInterruptedRuns(); len(resumed) != 0 {
		t.Fatalf("lost run must not resume: %v", resumed)
	}
	got, _ := s.Run(run.ID)
	if got.Status != "failed" || got.Nodes["fix"].Status != "failed" || !strings.Contains(got.Nodes["fix"].Error, "重跑") {
		t.Fatalf("run=%s node=%+v", got.Status, got.Nodes["fix"])
	}
	if got.WorkspaceHost != "worker-b-host" || got.WorkspaceRoot != worker.sandboxes.Root() {
		t.Fatalf("bind not moved for the rerun: %s:%s", got.WorkspaceHost, got.WorkspaceRoot)
	}
}

// 执行机器上子 SOP 用父流程的检出目录；节点不再要求执行机器推工作分支。
func TestRunnerChildUsesParentCheckoutWithoutBranchSync(t *testing.T) {
	t.Parallel()
	child := domain.Run{
		ID: "run-child",
		ExecutionContext: domain.TelemetryContext{
			RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared, ParentRunID: "run-parent", WorkspaceRunID: "run-root",
		},
		Repositories: []domain.RepositoryContext{{RepositoryID: "repo-svc", Name: "svc", CloneURL: "https://git.example.com/a/svc.git", BaseBranch: "master", WorkBranch: "feature/fix-1"}},
	}
	if got := runnerWorkspaceRel(child, "dev"); got != "runs/run-root/workspace" {
		t.Fatalf("child workspace = %s", got)
	}
	invocation := &domain.RunnerInvocation{}
	bindRunnerRepositories(invocation, child, "tok", "", "")
	for _, repository := range invocation.Repositories {
		if repository.SyncWorkBranch {
			t.Fatalf("repository %s still asks the runner to sync its work branch", repository.Name)
		}
	}
}

// 执行池机器离线：之前节点已改过代码就不能换机（改动在旧机器上），还没开始改就可以换。
func TestBindPoolRunnerRefusesSwitchAfterCodeChanges(t *testing.T) {
	previousWait := poolBindWait
	poolBindWait = 50 * time.Millisecond
	defer func() { poolBindWait = previousWait }()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e := New(s, flowruntime.NewManager(t.TempDir()))
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool=cloud", Projects: []string{"*"}})
	for _, r := range []domain.Runner{
		{ID: "gone", Name: "pool-a", Status: domain.RunnerStatusOffline, RunnerVersion: "0.4.0", Labels: []string{"pool=cloud"}},
		{ID: "fresh", Name: "pool-b", Status: domain.RunnerStatusOnline, RunnerVersion: "0.4.0", Labels: []string{"pool=cloud"}},
	} {
		if err := s.PutRunner(r); err != nil {
			t.Fatal(err)
		}
	}
	run := domain.Run{
		ID: "run-pool", Status: "running",
		ExecutionContext: domain.TelemetryContext{ProjectID: "p", ExecutionMode: domain.ExecutionModeRunner, ExecutionPool: domain.ExecutionModeCloud, ExecutionRunnerID: "gone"},
		Repository:       domain.RepositoryContext{RepositoryID: "repo-1", BaseBranch: "master", WorkBranch: "feature/x"},
		Nodes: map[string]*domain.NodeRun{
			"dev":    {NodeID: "dev", Status: "succeeded"},
			"review": {NodeID: "review", Status: "running"},
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.bindPoolRunner(context.Background(), run, "review"); err == nil || !strings.Contains(err.Error(), "pool-a") || !strings.Contains(err.Error(), "未恢复在线") {
		t.Fatalf("switch after code changes = %v", err)
	}
	run.Nodes["dev"].Status = "pending"
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	bound, err := e.bindPoolRunner(context.Background(), run, "dev")
	if err != nil || bound.ExecutionContext.ExecutionRunnerID != "fresh" {
		t.Fatalf("rerun from scratch must switch: runner=%q err=%v", bound.ExecutionContext.ExecutionRunnerID, err)
	}
}

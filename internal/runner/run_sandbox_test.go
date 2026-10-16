package runner

import (
	"context"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestSharedRunTasksReuseOneSandboxUntilCleanup(t *testing.T) {
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}}
	task := func(id, runID, workspace, provider string) domain.RunnerTask {
		return domain.RunnerTask{ID: id, RunID: runID, Workspace: workspace, Provider: provider, Sandbox: &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "p", Driver: "local"}}}
	}
	acquire := func(tk domain.RunnerTask) any {
		t.Helper()
		sandbox, release, err := d.acquireTaskSandbox(context.Background(), tk)
		if err != nil {
			t.Fatal(err)
		}
		release()
		return sandbox
	}
	first := acquire(task("t1", "run-1", "runs/run-1/workspace", "terminal"))
	if second := acquire(task("t2", "run-1", "runs/run-1/workspace", "terminal")); second != first {
		t.Fatal("second node of the run got a new sandbox")
	}
	if other := acquire(task("t3", "run-1", "runs/run-1/workspace", "codex")); other == first {
		t.Fatal("a different sandbox definition shared the container")
	}
	if legacy := acquire(task("t4", "run-1", "runs/run-1/node", "terminal")); legacy == first {
		t.Fatal("a per-node workspace shared the run sandbox")
	}
	if len(d.runSandboxes) != 2 {
		t.Fatalf("shared sandboxes = %d, want 2", len(d.runSandboxes))
	}
	d.closeRunSandboxes(context.Background(), "run-1")
	if len(d.runSandboxes) != 0 {
		t.Fatalf("cleanup left %d shared sandboxes", len(d.runSandboxes))
	}
	if again := acquire(task("t5", "run-1", "runs/run-1/workspace", "terminal")); again == first {
		t.Fatal("sandbox survived cleanup")
	}
}

// 失败后没人删除的运行不会收到 cleanup_run，空闲太久的共享沙箱要自动回收，正在用的不能回收。
func TestIdleSharedRunSandboxIsReaped(t *testing.T) {
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}}
	spec := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "p", Driver: "local"}}
	_, releaseIdle, err := d.acquireTaskSandbox(context.Background(), domain.RunnerTask{ID: "t1", RunID: "run-idle", Workspace: "runs/run-idle/workspace", Provider: "terminal", Sandbox: spec})
	if err != nil {
		t.Fatal(err)
	}
	releaseIdle()
	_, releaseBusy, err := d.acquireTaskSandbox(context.Background(), domain.RunnerTask{ID: "t2", RunID: "run-busy", Workspace: "runs/run-busy/workspace", Provider: "terminal", Sandbox: spec})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBusy()
	time.Sleep(5 * time.Millisecond)
	d.reapIdleRunSandboxes(context.Background(), time.Millisecond)
	if len(d.runSandboxes) != 1 {
		t.Fatalf("shared sandboxes after reap = %d, want 1", len(d.runSandboxes))
	}
	for _, entry := range d.runSandboxes {
		if entry.runID != "run-busy" {
			t.Fatalf("reaped the busy sandbox, kept %s", entry.runID)
		}
	}
	d.closeAllRunSandboxes(context.Background())
	if len(d.runSandboxes) != 0 {
		t.Fatalf("shutdown left %d shared sandboxes", len(d.runSandboxes))
	}
}

package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
)

// startPoolRunner registers a platform runner (bootstrap token, no owner)
// carrying the pool label. The returned function stops it.
func startPoolRunner(t *testing.T, s *repo.Store, serverURL, name string) (domain.Runner, context.CancelFunc) {
	t.Helper()
	return startPoolRunnerWithConfig(t, s, Config{
		ServerURL:            serverURL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		Name:                 name,
		Labels:               []string{"pool=cloud"},
		MaxConcurrency:       2,
		HeartbeatInterval:    time.Second,
		LongPollTimeout:      2 * time.Second,
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
}

func startPoolRunnerWithConfig(t *testing.T, s *repo.Store, cfg Config) (domain.Runner, context.CancelFunc) {
	t.Helper()
	daemon, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = daemon.Run(ctx) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("pool runner did not stop")
		}
	}
	t.Cleanup(stop)
	var runner domain.Runner
	waitFor(t, 5*time.Second, cfg.Name+" registration", func() bool {
		for _, r := range s.Runners() {
			if r.Name == cfg.Name && r.Status == domain.RunnerStatusOnline {
				runner = r
				return true
			}
		}
		return false
	})
	return runner, stop
}

// 控制面不再自己执行：没有个人执行机器的 run 等云端执行机器上线后在它上面跑完。
func TestPoolRequiredRunWaitsForPoolRunner(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	e.ConfigureCloudRunnerPool(engine.CloudRunnerPool{Label: "pool=cloud", Projects: []string{"*"}, Required: true})
	remote := bareRemoteWithBaseline(t)
	wf := domain.WorkflowDefinition{
		ID: "pool-required", Name: "Pool required", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "implement", Name: "实现", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "printf 'pool\\n' > feature.txt", MaxAttempts: 1},
		},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{RepositoryID: "repo", Name: "svc", CloneURL: remote, BaseBranch: "master", WorkBranch: "fix/pool"}, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if run.ExecutionContext.ExecutionPool != domain.ExecutionModeCloud || run.ExecutionContext.ExecutionMode != domain.ExecutionModeRunner || run.ExecutionContext.ExecutionRunnerID != "" {
		t.Fatalf("execution context before any pool runner = %+v", run.ExecutionContext)
	}
	time.Sleep(time.Second)
	if current, _ := s.Run(run.ID); current.Status != "running" {
		t.Fatalf("run did not wait for a pool runner: %s", current.Status)
	}
	runner, _ := startPoolRunner(t, s, server.URL, "builtin-a")
	waitFor(t, 30*time.Second, "pool run", func() bool {
		current, ok := s.Run(run.ID)
		return ok && (current.Status == "succeeded" || current.Status == "failed")
	})
	final, _ := s.Run(run.ID)
	if final.Status != "succeeded" || final.ExecutionContext.ExecutionRunnerID != runner.ID {
		t.Fatalf("status = %s on %q, implement error = %q", final.Status, final.ExecutionContext.ExecutionRunnerID, final.Nodes["implement"].Error)
	}
	if refs := runGitT(t, remote, "for-each-ref", "refs/heads/fix/pool"); refs != "" {
		t.Fatalf("a node must not push the work branch: %s", refs)
	}
}

// 部署期间 Worker 比 Runner 先恢复：等待原机器上线，保留上游未提交的代码并继续下游。
func TestPoolRunResumesOnOriginalRunnerAfterRestart(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	e.ConfigureCloudRunnerPool(engine.CloudRunnerPool{Label: "pool=cloud", Projects: []string{"*"}, Required: true})
	remote := bareRemoteWithBaseline(t)
	cfg := Config{
		ServerURL: server.URL, BootstrapToken: "boot-secret", Name: "builtin-a",
		Labels: []string{"pool=cloud"}, MaxConcurrency: 2,
		StatePath: filepath.Join(t.TempDir(), "state.json"), DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
		HeartbeatInterval: time.Hour, LongPollTimeout: 2 * time.Second,
	}
	first, stopFirst := startPoolRunnerWithConfig(t, s, cfg)
	wf := domain.WorkflowDefinition{
		ID: "pool-takeover", Name: "Pool takeover", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "implement", Name: "实现", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "printf 'from a\\n' > feature.txt", MaxAttempts: 1},
			{ID: "gate", Name: "确认", Type: "approval", MaxAttempts: 1},
			{ID: "verify", Name: "验证", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "test \"$(cat feature.txt)\" = 'from a' && printf 'from b\\n' >> feature.txt", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "implement-gate", Source: "implement", Target: "gate", When: "success"},
			{ID: "gate-verify", Source: "gate", Target: "verify", When: "success"},
		},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{RepositoryID: "repo", Name: "svc", CloneURL: remote, BaseBranch: "master", WorkBranch: "fix/takeover"}, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if run.ExecutionContext.ExecutionRunnerID != first.ID {
		t.Fatalf("run bound to %q, want %s", run.ExecutionContext.ExecutionRunnerID, first.ID)
	}
	waitFor(t, 30*time.Second, "approval wait", func() bool {
		current, _ := s.Run(run.ID)
		return current.Status == "waiting"
	})
	stopFirst()
	if stopped, _ := s.Runner(first.ID); stopped.Status != domain.RunnerStatusOffline {
		t.Fatalf("stopped runner presence = %s", stopped.Status)
	}
	startPoolRunner(t, s, server.URL, "builtin-b")
	if err := e.Approve(run.ID, "gate", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "original runner wait", func() bool {
		current, _ := s.Run(run.ID)
		for _, event := range current.Events {
			if event.Type == "execution.waiting" && strings.Contains(event.Message, first.Name) {
				return true
			}
		}
		return false
	})
	if current, _ := s.Run(run.ID); current.Status != "running" || current.ExecutionContext.ExecutionRunnerID != first.ID {
		t.Fatalf("run must wait on original runner: %s on %s", current.Status, current.ExecutionContext.ExecutionRunnerID)
	}
	restarted, _ := startPoolRunnerWithConfig(t, s, cfg)
	if restarted.ID != first.ID {
		t.Fatalf("restart changed runner ID from %s to %s", first.ID, restarted.ID)
	}
	waitFor(t, 30*time.Second, "run after runner loss", func() bool {
		current, _ := s.Run(run.ID)
		return current.Status == "succeeded" || current.Status == "failed"
	})
	final, _ := s.Run(run.ID)
	if final.Status != "succeeded" || final.ExecutionContext.ExecutionRunnerID != first.ID || final.Nodes["implement"].Attempt != 1 || final.Nodes["verify"].Attempt != 1 {
		t.Fatalf("status = %s on %q, verify error = %q", final.Status, final.ExecutionContext.ExecutionRunnerID, final.Nodes["verify"].Error)
	}
	if refs := runGitT(t, remote, "for-each-ref", "refs/heads/fix/takeover"); refs != "" {
		t.Fatalf("a node must not push the work branch: %s", refs)
	}
}

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
	daemon, err := New(Config{
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
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = daemon.Run(ctx) }()
	var runner domain.Runner
	waitFor(t, 5*time.Second, name+" registration", func() bool {
		for _, r := range s.Runners() {
			if r.Name == name && r.Status == domain.RunnerStatusOnline {
				runner = r
				return true
			}
		}
		return false
	})
	return runner, cancel
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

// 云端执行机器掉线时，之前节点的代码改动只在那台机器上：不换机，直接失败并提示重跑。
func TestPoolRunFailsWhenItsRunnerGoesOfflineAfterCodeChanges(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	e.ConfigureCloudRunnerPool(engine.CloudRunnerPool{Label: "pool=cloud", Projects: []string{"*"}, Required: true})
	remote := bareRemoteWithBaseline(t)
	first, stopFirst := startPoolRunner(t, s, server.URL, "builtin-a")
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
	waitFor(t, 10*time.Second, "first runner offline", func() bool {
		r, _ := s.Runner(first.ID)
		if r.Status == domain.RunnerStatusOnline {
			r.Status = domain.RunnerStatusOffline
			_ = s.PutRunner(r)
		}
		return r.Status != domain.RunnerStatusOnline
	})
	startPoolRunner(t, s, server.URL, "builtin-b")
	if err := e.Approve(run.ID, "gate", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "run after runner loss", func() bool {
		current, _ := s.Run(run.ID)
		return current.Status == "succeeded" || current.Status == "failed"
	})
	final, _ := s.Run(run.ID)
	if final.Status != "failed" || final.ExecutionContext.ExecutionRunnerID != first.ID || !strings.Contains(final.Nodes["verify"].Error, "builtin-a 已离线") {
		t.Fatalf("status = %s on %q, verify error = %q", final.Status, final.ExecutionContext.ExecutionRunnerID, final.Nodes["verify"].Error)
	}
	if refs := runGitT(t, remote, "for-each-ref", "refs/heads/fix/takeover"); refs != "" {
		t.Fatalf("a node must not push the work branch: %s", refs)
	}
}

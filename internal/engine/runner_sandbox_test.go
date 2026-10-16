package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestRunnerSandboxSpecCarriesResolvedDockerAssets(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dockerfile := domain.DockerfileDefinition{ID: "runner-image", Name: "Runner image", Content: "FROM alpine:3.20\n"}
	profile := domain.SandboxProfile{ID: "runner-docker", Name: "Runner Docker", Driver: "docker", DockerfileID: dockerfile.ID, Environment: map[string]string{"E2E": "1"}}
	if err := s.PutDockerfile(dockerfile); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSandbox(profile); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	spec, err := e.runnerSandboxSpec(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: profile.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if spec == nil || spec.Profile.Driver != "docker" || spec.Profile.Environment["E2E"] != "1" || spec.Dockerfile == nil || spec.Dockerfile.Content != dockerfile.Content {
		t.Fatalf("runner sandbox spec = %+v", spec)
	}
}

func TestRunnerSandboxSpecUsesFrozenSnapshot(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutDockerfile(domain.DockerfileDefinition{ID: "image", Name: "changed", Content: "FROM busybox\n"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "docker", Name: "changed", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	frozen := &domain.RunnerSandboxSpec{
		Profile:    domain.SandboxProfile{ID: "docker", Name: "original", Driver: "docker", DockerfileID: "image", Environment: map[string]string{"SNAPSHOT": "yes"}},
		Dockerfile: &domain.DockerfileDefinition{ID: "image", Name: "original", Content: "FROM alpine:3.20\n"},
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	spec, err := e.runnerSandboxSpec(domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "docker", SandboxSnapshot: frozen}})
	if err != nil {
		t.Fatal(err)
	}
	if spec == nil || spec.Profile.Driver != "docker" || spec.Profile.Environment["SNAPSHOT"] != "yes" || spec.Dockerfile == nil || spec.Dockerfile.Content != "FROM alpine:3.20\n" {
		t.Fatalf("runner sandbox did not use frozen snapshot: %+v", spec)
	}
}

func TestDefaultExecutionRunnerUsesFrozenTarget(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"runner-original", "runner-new"} {
		if err := s.PutRunner(domain.Runner{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetDefaultRunner(42, "runner-new", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	run := domain.Run{ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 42, ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner-original"}}
	if got := e.defaultExecutionRunner(run); got != "runner-original" {
		t.Fatalf("runner target drifted to current default: %q", got)
	}
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeCloud
	if got := e.defaultExecutionRunner(run); got != "" {
		t.Fatalf("cloud snapshot unexpectedly selected runner %q", got)
	}
}

func TestResumePausedWaitsForActiveRunnerTask(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	run := domain.Run{ID: "run-paused-remote", WorkflowID: "wf", Status: "waiting", PauseRequested: true, Nodes: map[string]*domain.NodeRun{
		"work": {NodeID: "work", Status: "waiting_approval", Activity: "已由用户暂停，等待补充信息"},
	}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRunnerTask(domain.RunnerTask{ID: "rt-active", RunnerID: "runner-a", Provider: "codex", RunID: run.ID, Status: domain.RunnerTaskRunning}); err != nil {
		t.Fatal(err)
	}
	err = e.ResumePaused(run.ID, "continue")
	if err == nil || !strings.Contains(err.Error(), "执行机器仍在停止") {
		t.Fatalf("resume error=%v", err)
	}
}

func TestRunnerWorkspaceRelUsesFrozenLayout(t *testing.T) {
	shared := domain.Run{
		ID: "run-shared",
		ExecutionContext: domain.TelemetryContext{
			RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		},
	}
	if got := runnerWorkspaceRel(shared, "analyze"); got != "runs/run-shared/workspace" {
		t.Fatalf("shared runner workspace = %q", got)
	}
	legacy := domain.Run{ID: "run-legacy"}
	if got := runnerWorkspaceRel(legacy, "analyze"); got != "runs/run-legacy/analyze" {
		t.Fatalf("legacy runner workspace = %q", got)
	}
}

func TestShellSingleQuotePreservesPythonScript(t *testing.T) {
	script := "print(\"it's shared\")\nprint('done')"
	got := shellSingleQuote(script)
	want := `'print("it'"'"'s shared")
print('"'"'done'"'"')'`
	if got != want {
		t.Fatalf("shell quote = %q, want %q", got, want)
	}
}

func TestBindRunnerRepositoriesPreservesMultiRepositoryLayout(t *testing.T) {
	run := domain.Run{
		Repositories: []domain.RepositoryContext{
			{RepositoryID: "frontend", CloneURL: "https://git.example/frontend.git", BaseBranch: "main", WorkBranch: "feature/issue-7", WorkspaceDir: "frontend"},
			{RepositoryID: "backend", CloneURL: "https://git.example/backend.git", BaseBranch: "release", WorkBranch: "feature/issue-7", WorkspaceDir: "backend"},
		},
	}
	invocation := &domain.RunnerInvocation{}
	bindRunnerRepositories(invocation, run, "git-token", "example-admin", "example-admin@users.noreply.loopforge.invalid")
	if invocation.GitUserName != "example-admin" || invocation.GitUserEmail != "example-admin@users.noreply.loopforge.invalid" {
		t.Fatalf("runner git identity = %q <%s>", invocation.GitUserName, invocation.GitUserEmail)
	}
	if len(invocation.Repositories) != 2 {
		t.Fatalf("runner repositories = %+v", invocation.Repositories)
	}
	for i, wantDir := range []string{"frontend", "backend"} {
		got := invocation.Repositories[i]
		if got.Dir != wantDir || got.WorkBranch != "feature/issue-7" || got.GitToken != "git-token" {
			t.Fatalf("runner repository %d = %+v", i, got)
		}
	}
}

func TestRetryFailsBeforeMutationWhenFrozenRunnerIsOffline(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runner := domain.Runner{
		ID: "runner-offline", Name: "offline host", Status: domain.RunnerStatusOffline,
		Runtimes: []domain.RunnerRuntime{{Provider: "terminal", Status: domain.RuntimeStatusOnline}},
	}
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "run-offline-retry", WorkflowID: "retry", Status: "failed",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "retry", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "work", Type: "command", Command: "true", MaxAttempts: 1}}},
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID},
		Nodes:            map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "failed", Attempt: 1, Error: "lost connection"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	e.ConfigureExecution("", "", 0, false)
	err = e.Retry(run.ID, "work", "retry", "")
	if err == nil || !strings.Contains(err.Error(), "无法重连原执行环境") || !strings.Contains(err.Error(), "当前离线") {
		t.Fatalf("retry error = %v", err)
	}
	current, _ := s.Run(run.ID)
	if current.Status != "failed" || current.Nodes["work"].Status != "failed" || current.Nodes["work"].Attempt != 1 {
		t.Fatalf("failed reconnect mutated run: %+v", current)
	}
}

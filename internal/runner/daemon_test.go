package runner

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/httpapi"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestBindInvocationCredentials(t *testing.T) {
	t.Run("local login leaves codex invocation unchanged", func(t *testing.T) {
		argv := []string{"codex", "exec", "prompt"}
		gotArgv, env, err := bindInvocationCredentials(&domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthLocal}, []runtime.CredentialBinding{runtime.BindCodex}, argv)
		if err != nil || len(env) != 0 || strings.Join(gotArgv, "|") != strings.Join(argv, "|") {
			t.Fatalf("argv=%v env=%v err=%v", gotArgv, env, err)
		}
	})
	t.Run("personal codex injects key and provider config", func(t *testing.T) {
		inv := &domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthPersonal, CodexAPIKey: "codex-secret", CodexBaseURL: "https://gateway.example/v1"}
		argv, env, err := bindInvocationCredentials(inv, []runtime.CredentialBinding{runtime.BindCodex}, []string{"codex", "exec", "prompt"})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(argv, " ")
		if env["CODEX_API_KEY"] != "codex-secret" || !strings.Contains(joined, "gateway.example/v1") || argv[len(argv)-1] != "prompt" {
			t.Fatalf("argv=%v env=%v", argv, env)
		}
	})
	t.Run("personal claude injects key and base URL", func(t *testing.T) {
		inv := &domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthPersonal, ClaudeCodeAPIKey: "claude-secret", ClaudeCodeBaseURL: "https://claude.example"}
		_, env, err := bindInvocationCredentials(inv, []runtime.CredentialBinding{runtime.BindClaudeCode}, []string{"claude", "-p", "prompt"})
		if err != nil || env["ANTHROPIC_API_KEY"] != "claude-secret" || env["ANTHROPIC_BASE_URL"] != "https://claude.example" {
			t.Fatalf("env=%v err=%v", env, err)
		}
	})
}

func TestBindSandboxPrivateHomeUsesContainerPaths(t *testing.T) {
	workspace := t.TempDir()
	env, err := bindSandboxPrivateHome(workspace, "docker", "codex", map[string]string{"CODEX_API_KEY": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if env["HOME"] != "/workspace/.agentflow/private-home/codex" || env["CODEX_HOME"] != "/workspace/.agentflow/private-home/codex/.codex" {
		t.Fatalf("container private home = %+v", env)
	}
	if env["CODEX_API_KEY"] != "secret" {
		t.Fatal("credential environment was lost")
	}
}

func TestWorkspaceContainsPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runs", "run-1")
	if !workspaceContainsPath(root, filepath.Join(root, "node-a")) {
		t.Fatal("node workspace should belong to run workspace")
	}
	if !workspaceContainsPath(root, root) {
		t.Fatal("run workspace should contain itself")
	}
	if workspaceContainsPath(root, filepath.Join(filepath.Dir(root), "run-10", "node-a")) {
		t.Fatal("sibling run workspace must not match by string prefix")
	}
}

func TestWriteRunnerHumanContext(t *testing.T) {
	workspace := t.TempDir()
	if err := writeRunnerHumanContext(workspace, "operator fact"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "human-context.md"))
	if err != nil || !strings.Contains(string(raw), "operator fact") {
		t.Fatalf("human context file=%q err=%v", raw, err)
	}
	if err := writeRunnerHumanContext(workspace, ""); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(workspace, "human-context.md"))
	if err != nil || !strings.Contains(string(raw), "operator fact") {
		t.Fatalf("empty context should not delete a workspace file, file=%q err=%v", raw, err)
	}
}

// newDaemonTestServer starts a real control-plane handler backed by the
// in-memory store, with runner registration enabled.
func newDaemonTestServer(t *testing.T) (*httptest.Server, *repo.Store, *engine.Engine) {
	t.Helper()
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := httpapi.New(s, e, tracker.NewService(), "", "")
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "boot-secret")
	return server, s, e
}

func putMockDeveloperPlaybook(t *testing.T, s *repo.Store) {
	t.Helper()
	agent, ok := s.Agent("developer")
	if !ok {
		t.Fatal("developer role missing")
	}
	agent.ExecutorID = "mock"
	agent.TaskPrompt = &domain.TaskPromptSpec{Objective: "{{input}}"}
	if err := s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
}

// waitFor polls until cond is true or the deadline expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDeleteFailedRunCleansRunnerWorkspace(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	workspaceRoot := filepath.Join(t.TempDir(), "runner-workspace")
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		Name:                 "delete-cleanup-runner",
		MaxConcurrency:       1,
		HeartbeatInterval:    time.Second,
		LongPollTimeout:      2 * time.Second,
		DefaultWorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = daemon.Run(ctx) }()
	waitFor(t, 5*time.Second, "runner registration", func() bool { return len(s.Runners()) == 1 })
	runnerID := s.Runners()[0].ID
	run := domain.Run{
		ID:     "failed-delete-cleanup",
		Status: "failed",
		ExecutionContext: domain.TelemetryContext{
			ExecutionMode:     domain.ExecutionModeRunner,
			ExecutionRunnerID: runnerID,
		},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(workspaceRoot, "runs", run.ID)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "partial.txt"), []byte("failed output"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.DeleteRun(run.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Run(run.ID); ok {
		t.Fatal("run record was not deleted")
	}
	waitFor(t, 10*time.Second, "runner workspace cleanup after delete", func() bool {
		task, ok := s.RunnerTask("rt-cleanup-" + run.ID)
		if !ok || task.Status != domain.RunnerTaskSuccess {
			return false
		}
		_, statErr := os.Stat(workspace)
		return os.IsNotExist(statErr)
	})
}

func TestWorkflowScriptsShareRunnerRunWorkspace(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	repository := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repository, "vendor", "tracked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "vendor", "tracked", "dependency.php"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"},
		{"config", "user.email", "runner-test@example.com"},
		{"config", "user.name", "Runner Test"},
		{"add", "vendor/tracked/dependency.php"},
		{"commit", "-qm", "baseline"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repository}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		Name:                 "shared-script-runner",
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
		ID: "runner-shared-scripts", Name: "Runner shared scripts", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "prepare", Name: "Prepare", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "test \"$(git branch --show-current)\" = fix/shared-runner-workspace && mkdir -p .git/info vendor/new && printf '/vendor/\\n' >> .git/info/exclude && git ls-files -z -- vendor/ | git update-index --skip-worktree -z --stdin && printf 'changed\\n' > vendor/tracked/dependency.php && printf 'new\\n' > vendor/new/dependency.php && printf 'shared-ready\\n' > workspace-marker", MaxAttempts: 1},
			{ID: "consume", Name: "Consume", Type: "script", ScriptLanguage: "python", ExecutionScope: domain.ExecutionScopeRepositories, Script: "from pathlib import Path\nimport subprocess\nassert subprocess.check_output(['git', 'status', '--short', '--', 'vendor']).decode() == ''\nprint(Path(\"workspace-marker\").read_text().strip() + \" it's visible\")", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "prepare-consume", Source: "prepare", Target: "consume", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{RepositoryID: "repo", CloneURL: repository, BaseBranch: "master", WorkBranch: "fix/shared-runner-workspace"}, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "shared runner script workflow", func() bool {
		current, ok := s.Run(run.ID)
		return ok && (current.Status == "succeeded" || current.Status == "failed")
	})
	final, _ := s.Run(run.ID)
	if final.Status != "succeeded" {
		var events []domain.RunnerTaskEvent
		for _, task := range s.RunnerTasks() {
			if task.RunID == run.ID {
				events = append(events, s.RunnerTaskEvents(task.ID)...)
			}
		}
		t.Fatalf("script workflow status = %s, prepare error = %q, consume error = %q, events = %+v", final.Status, final.Nodes["prepare"].Error, final.Nodes["consume"].Error, events)
	}
	if final.ExecutionContext.RunnerWorkspaceLayout != domain.RunnerWorkspaceLayoutShared {
		t.Fatalf("runner workspace layout was not frozen: %+v", final.ExecutionContext)
	}
	if output := final.Nodes["consume"].Output; !strings.Contains(output, "shared-ready it's visible") {
		t.Fatalf("downstream script did not see shared workspace: %q", output)
	}
	wantWorkspace := filepath.ToSlash(filepath.Join("runs", run.ID, "workspace"))
	var scriptTasks int
	for _, task := range s.RunnerTasks() {
		if task.RunID != run.ID || task.Action != "" {
			continue
		}
		scriptTasks++
		if task.Provider != "terminal" || filepath.ToSlash(task.Workspace) != wantWorkspace {
			t.Fatalf("script task did not use shared runner workspace: %+v", task)
		}
	}
	if scriptTasks != 2 {
		t.Fatalf("runner script task count = %d, want 2", scriptTasks)
	}
}

func TestConfigureRepositoryGitIdentityOverridesStaleLocalConfig(t *testing.T) {
	repository := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"},
		{"config", "--local", "user.name", "Stale User"},
		{"config", "--local", "user.email", "stale@example.com"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", repository}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := configureRepositoryGitIdentity(context.Background(), repository, "example-admin", "example-admin@users.noreply.loopforge.invalid"); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"user.name": "example-admin", "user.email": "example-admin@users.noreply.loopforge.invalid"} {
		output, err := exec.Command("git", "-C", repository, "config", "--local", key).Output()
		if err != nil || strings.TrimSpace(string(output)) != want {
			t.Fatalf("%s = %q, want %q (err=%v)", key, output, want, err)
		}
	}
}

func TestCheckoutInvocationRepositoriesProtectsEachSandboxedRepository(t *testing.T) {
	root := t.TempDir()
	invocation := &domain.RunnerInvocation{}
	for _, name := range []string{"frontend", "backend"} {
		repository := filepath.Join(root, name+"-source")
		if err := os.MkdirAll(filepath.Join(repository, ".codebuddy"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository, "AGENTS.md"), []byte("project instructions\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository, ".codebuddy", "settings.json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"init", "-q", "-b", "master"},
			{"config", "user.email", "runner-test@example.com"},
			{"config", "user.name", "Runner Test"},
			{"add", "AGENTS.md", ".codebuddy/settings.json"},
			{"commit", "-qm", "baseline"},
		} {
			cmd := exec.Command("git", append([]string{"-C", repository}, args...)...)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %s %v: %v: %s", name, args, err, output)
			}
		}
		invocation.Repositories = append(invocation.Repositories, domain.RunnerRepository{CloneURL: repository, Ref: "master", Dir: name})
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkoutInvocationRepositories(context.Background(), workspace, invocation, "docker"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"frontend", "backend"} {
		repository := filepath.Join(workspace, name)
		cmd := exec.Command("git", "-C", repository, "ls-files", "-v", "--", "AGENTS.md", ".codebuddy/settings.json")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git ls-files %s: %v: %s", name, err, output)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if !strings.HasPrefix(line, "S ") {
				t.Fatalf("%s repository path is not skip-worktree protected: %q", name, line)
			}
		}
		exclude, err := os.ReadFile(filepath.Join(repository, ".git", "info", "exclude"))
		if err != nil || !strings.Contains(string(exclude), ".agents/") {
			t.Fatalf("%s repository excludes were not installed: %v %q", name, err, exclude)
		}
	}
}

func TestDaemonExecutesMockTaskEndToEnd(t *testing.T) {
	server, s, _ := newDaemonTestServer(t)

	statePath := filepath.Join(t.TempDir(), "state.json")
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            statePath,
		Name:                 "test-daemon",
		MaxConcurrency:       1,
		HeartbeatInterval:    time.Second,
		LongPollTimeout:      2 * time.Second,
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()

	// The runner registers with its mock runtime.
	waitFor(t, 5*time.Second, "runner registration", func() bool {
		runners := s.Runners()
		return len(runners) == 1 && runners[0].Status == domain.RunnerStatusOnline
	})

	// Create a task through the management API.
	task := domain.RunnerTask{
		ID: "rt-e2e", Provider: mockProviderKind, Prompt: "hello end to end",
		Status: domain.RunnerTaskQueued, MaxAttempts: 1,
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}

	// The daemon claims it, executes the mock provider, and completes it.
	waitFor(t, 15*time.Second, "task completion", func() bool {
		final, ok := s.RunnerTask("rt-e2e")
		return ok && final.Status == domain.RunnerTaskSuccess
	})
	final, _ := s.RunnerTask("rt-e2e")
	if final.Result != "MOCK hello end to end" {
		t.Fatalf("unexpected result: %q", final.Result)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("unexpected exit code: %+v", final.ExitCode)
	}
	// Execution events were reported with seq keys.
	events := s.RunnerTaskEvents("rt-e2e")
	if len(events) == 0 {
		t.Fatal("no execution events reported")
	}

	// State was persisted so a restart reuses the same runner id.
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		RunnerID string `json:"runnerId"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.RunnerID == "" {
		t.Fatal("state file missing runner id")
	}
	cancel()
}

func TestDaemonExecuteProcessRunsRealCommand(t *testing.T) {
	server, s, _ := newDaemonTestServer(t)
	_ = server

	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Register the daemon so its client has a session, then drive
	// executeProcess directly with a provider that definitely exists: the
	// test binary itself invoked through `go`... simpler: use /bin/echo via a
	// synthetic runtime entry and a custom provider through ExtraBinaries.
	daemon, err = New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
		ExtraBinaries:        map[string]string{"echo": "echo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := daemon.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}

	task := domain.RunnerTask{
		ID: "rt-echo", Provider: "echo", Prompt: "direct exec", Status: domain.RunnerTaskQueued, MaxAttempts: 1,
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.ClaimRunnerTask(daemon.runnerID(), []string{"echo"}, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	// "echo" is a registered ExtraBinaries provider with no template, so
	// executeTask will report "provider not supported"; drive the real
	// process path directly with an explicit argv instead, then complete the
	// task the same way executeTask would.
	argv := []string{"echo", "hello-process"}
	result := daemon.executeProcess(ctx, claimed, argv)
	if result.status != domain.RunnerTaskSuccess {
		t.Fatalf("unexpected status %q err %q", result.status, result.errMsg)
	}
	if result.exitCode != 0 {
		t.Fatalf("exit code = %d", result.exitCode)
	}
	if !strings.Contains(result.result, "hello-process") {
		t.Fatalf("missing command stdout in result %q", result.result)
	}
	if strings.Contains(result.result, "file already closed") {
		t.Fatalf("stream error leaked into result %q", result.result)
	}
	if err := s.CompleteRunnerTask("rt-echo", daemon.runnerID(), domain.RunnerTaskSuccess, &result.exitCode, result.result, result.errMsg, nil, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	final, _ := s.RunnerTask("rt-echo")
	if final.Status != domain.RunnerTaskSuccess {
		t.Fatalf("final task status = %s", final.Status)
	}
	events := s.RunnerTaskEvents("rt-echo")
	if len(events) == 0 {
		t.Fatal("no events streamed for real process")
	}
}

func TestExecuteProcessCapturesFastCommandStdout(t *testing.T) {
	server, s, _ := newDaemonTestServer(t)
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
		ExtraBinaries:        map[string]string{"echo": "echo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := daemon.ensureRegistered(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		id := "rt-fast-" + strconv.Itoa(i)
		task := domain.RunnerTask{
			ID: id, Provider: "echo", Prompt: "fast", Status: domain.RunnerTaskQueued, MaxAttempts: 1,
		}
		if err := s.PutRunnerTask(task); err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := s.ClaimRunnerTask(daemon.runnerID(), []string{"echo"}, time.Now().UTC(), time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim %s: ok=%v err=%v", id, ok, err)
		}
		result := daemon.executeProcess(ctx, claimed, []string{"/bin/sh", "-lc", "echo gate-ran-on-runner"})
		if result.status != domain.RunnerTaskSuccess {
			t.Fatalf("iter %d status=%q err=%q result=%q", i, result.status, result.errMsg, result.result)
		}
		if !strings.Contains(result.result, "gate-ran-on-runner") {
			t.Fatalf("iter %d missing stdout: %q", i, result.result)
		}
		if strings.Contains(result.result, "file already closed") {
			t.Fatalf("iter %d leaked stream error into result: %q", i, result.result)
		}
		if err := s.CompleteRunnerTask(id, daemon.runnerID(), domain.RunnerTaskSuccess, &result.exitCode, result.result, result.errMsg, nil, nil, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExecuteProcessStoresVisibleTextNotStreamJSON(t *testing.T) {
	server, s, _ := newDaemonTestServer(t)
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.ensureRegistered(context.Background()); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{
		ID: "rt-visible", Provider: "codebuddy", Status: domain.RunnerTaskQueued, MaxAttempts: 1,
		Invocation: &domain.RunnerInvocation{Executor: domain.ExecutorDefinition{Kind: "codebuddy", Protocol: "stream-json", Model: "test"}},
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.ClaimRunnerTask(daemon.runnerID(), []string{"codebuddy"}, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	script := `printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"plain answer"}]}}' '{"type":"result","result":"final answer","is_error":false}'`
	result := daemon.executeProcess(context.Background(), claimed, []string{"/bin/sh", "-c", script})
	if result.status != domain.RunnerTaskSuccess {
		t.Fatalf("status=%s err=%s", result.status, result.errMsg)
	}
	if result.result != "final answer" {
		t.Fatalf("node output = %q", result.result)
	}
	if strings.Contains(result.result, `"type":"assistant"`) || strings.Contains(result.result, `\"`) {
		t.Fatalf("stream envelope leaked into node output: %q", result.result)
	}
}

func TestDaemonStreamsAndNormalizesSparseProviderOutput(t *testing.T) {
	server, s, _ := newDaemonTestServer(t)
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.ensureRegistered(context.Background()); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{
		ID: "rt-observable", Provider: "codex", Status: domain.RunnerTaskQueued, MaxAttempts: 1,
		Invocation: &domain.RunnerInvocation{Executor: domain.ExecutorDefinition{Kind: "codex", Protocol: "jsonl", Model: "gpt-test"}},
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.ClaimRunnerTask(daemon.runnerID(), []string{"codex"}, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	done := make(chan execResult, 1)
	line := `{"type":"turn.completed","thread_id":"thread-remote","usage":{"input_tokens":12,"output_tokens":5}}`
	go func() {
		done <- daemon.executeProcess(context.Background(), claimed, []string{"/bin/sh", "-c", "printf '%s\\n' '" + line + "'; printf 'provider warning\\n' >&2; sleep 2"})
	}()

	// Sparse output must be visible while the process is still running; it
	// must not wait for the old 16 KiB threshold or process completion.
	waitFor(t, 1500*time.Millisecond, "incremental runner output", func() bool {
		for _, event := range s.RunnerTaskEvents(task.ID) {
			if event.Type == "stdout" && strings.Contains(event.Data, "turn.completed") {
				return true
			}
		}
		return false
	})
	select {
	case <-done:
		t.Fatal("process completed before incremental-output assertion")
	default:
	}

	result := <-done
	if result.status != domain.RunnerTaskSuccess {
		t.Fatalf("status=%s err=%s", result.status, result.errMsg)
	}
	foundUsage, foundStderr, foundCompleted := false, false, false
	for _, event := range s.RunnerTaskEvents(task.ID) {
		if event.Type != "observation" {
			continue
		}
		var observation domain.TelemetryEvent
		if json.Unmarshal([]byte(event.Data), &observation) == nil && observation.Category == "usage" && observation.Usage != nil && observation.Usage.InputTokens == 12 {
			foundUsage = true
		}
		if observation.Name == "process.stderr" && observation.Message == "provider warning" {
			foundStderr = true
		}
		if observation.Name == "agent.completed" {
			foundCompleted = true
		}
	}
	if !foundUsage {
		t.Fatal("normalized usage observation missing from runner event stream")
	}
	if !foundStderr || !foundCompleted {
		t.Fatalf("terminal observations missing: stderr=%v completed=%v", foundStderr, foundCompleted)
	}
}

// TestWorkflowAgentNodeDispatchesToRunner is the headline integration case:
// a role bound to a runner machine (AgentDefinition.RunnerID) executes its
// workflow Agent node on the runner daemon, and the run finishes successfully
// with the runner result wired back into the node.
func TestWorkflowAgentNodeDispatchesToRunner(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	agent, found := s.Agent("developer")
	if !found {
		t.Fatal("seed agent developer missing")
	}
	agent.ExecutorID = "mock"
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		Name:                 "workflow-runner",
		MaxConcurrency:       1,
		HeartbeatInterval:    time.Second,
		LongPollTimeout:      2 * time.Second,
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := daemon.cfg.DefaultWorkspaceRoot
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()
	waitFor(t, 5*time.Second, "runner registration", func() bool {
		runners := s.Runners()
		return len(runners) == 1 && runners[0].Status == domain.RunnerStatusOnline
	})

	// The run's responsible user selects the runner as their default
	// execution machine: routing is a user-level decision.
	runner := s.Runners()[0]
	runner.OwnerUserID = 42
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultRunner(42, runner.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	putMockDeveloperPlaybook(t, s)
	wf := domain.WorkflowDefinition{
		ID: "runner-e2e", Name: "Runner e2e", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "agent", AgentID: "developer", ExecutorID: "mock", MaxAttempts: 1}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "runner integration input", domain.RepositoryContext{}, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "run completion", func() bool {
		current, _ := s.Run(run.ID)
		return current.Status == "succeeded"
	})
	waitFor(t, 10*time.Second, "runner workspace immediate cleanup", func() bool {
		_, statErr := os.Stat(filepath.Join(workspaceRoot, "runs", run.ID))
		current, ok := s.Run(run.ID)
		return ok && os.IsNotExist(statErr) && !current.WorkspaceCleanedAt.IsZero()
	})
	final, _ := s.Run(run.ID)
	node := final.Nodes["work"]
	if node == nil || node.Status != "succeeded" {
		t.Fatalf("unexpected node state: %+v", node)
	}
	if !strings.Contains(node.Output, "runner integration input") {
		t.Fatalf("node output missing runner result: %q", node.Output)
	}
	// The dispatch created a workflow-linked runner task.
	allTasks := s.RunnerTasks()
	tasks := make([]domain.RunnerTask, 0, len(allTasks))
	for _, task := range allTasks {
		if task.Action == "" {
			tasks = append(tasks, task)
		}
	}
	if len(tasks) != 1 {
		t.Fatalf("expected exactly one execution task, got %d (%d including cleanup)", len(tasks), len(allTasks))
	}
	if tasks[0].RunID != run.ID || tasks[0].NodeID != "work" || tasks[0].Status != domain.RunnerTaskSuccess {
		t.Fatalf("unexpected runner task: %+v", tasks[0])
	}
	// The dispatch is visible in the run timeline.
	foundDispatch := false
	for _, ev := range final.Telemetry {
		if ev.Name == "runner.dispatch" {
			foundDispatch = true
		}
	}
	if !foundDispatch {
		t.Fatal("runner.dispatch telemetry missing from run timeline")
	}
	cancel()
}

func TestRunnerPauseWaitsForRemoteStopAndRestartsScriptInSameWorkspace(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		Name:                 "pause-runner",
		MaxConcurrency:       1,
		HeartbeatInterval:    time.Second,
		LongPollTimeout:      2 * time.Second,
		DefaultWorkspaceRoot: workspaceRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()
	waitFor(t, 5*time.Second, "runner registration", func() bool {
		return len(s.Runners()) == 1 && s.Runners()[0].Status == domain.RunnerStatusOnline
	})
	waitFor(t, 5*time.Second, "runner websocket control connection", func() bool {
		return daemon.wsOnline.Load()
	})
	runner := s.Runners()[0]
	runner.OwnerUserID = 42
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultRunner(42, runner.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "runner-pause", Name: "Runner pause", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{
		ID: "work", Name: "Work", Type: "command", Command: "if test -f .script-started; then printf restarted; else touch .script-started; sleep 30; fi", MaxAttempts: 1,
	}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "pause remote", domain.RepositoryContext{}, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "remote task running with persistent marker", func() bool {
		active, _ := s.ActiveRunnerTasksForRun(run.ID)
		if len(active) != 1 || active[0].Status != domain.RunnerTaskRunning {
			return false
		}
		_, statErr := os.Stat(filepath.Join(workspaceRoot, filepath.FromSlash(active[0].Workspace), ".script-started"))
		return statErr == nil
	})
	pauseStarted := time.Now()
	if err := e.Pause(run.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "remote process stopped", func() bool {
		current, _ := s.Run(run.ID)
		active, _ := s.ActiveRunnerTasksForRun(run.ID)
		return current.Status == "waiting" && current.PauseRequested && len(active) == 0
	})
	if elapsed := time.Since(pauseStarted); elapsed >= 5*time.Second {
		t.Fatalf("websocket pause took %s; expected server push before HTTP fallback", elapsed)
	}
	if err := e.ResumePaused(run.ID, "this must not reach a script"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "resumed remote run", func() bool {
		current, _ := s.Run(run.ID)
		return current.Status == "succeeded"
	})
	final, _ := s.Run(run.ID)
	if final.HumanContext != "" || final.Nodes["work"].Status != "succeeded" || !strings.Contains(final.Nodes["work"].Output, "restarted") {
		t.Fatalf("resumed remote run=%+v node=%+v", final, final.Nodes["work"])
	}
}

// TestRunnerBoundRoleRunsGatesOnRunnerMachine verifies the terminal
// capability: when a role is bound to a runner, its verification gates also
// execute on that machine, inside the same task workspace as the CLI run.
func TestRunnerBoundRoleRunsGatesOnRunnerMachine(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	agent, found := s.Agent("developer")
	if !found {
		t.Fatal("seed agent developer missing")
	}
	agent.ExecutorID = "mock"
	agent.Verification = []domain.VerificationGate{{ID: "check", Name: "检查", Mode: "command", Commands: []string{"echo gate-ran-on-runner"}, Required: true, ExecutionScope: domain.ExecutionScopeRun}}
	if err := s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	daemon, err := New(Config{
		ServerURL:            server.URL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		Name:                 "gate-runner",
		MaxConcurrency:       1,
		HeartbeatInterval:    time.Second,
		LongPollTimeout:      2 * time.Second,
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceRoot := daemon.cfg.DefaultWorkspaceRoot
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = daemon.Run(ctx) }()
	waitFor(t, 5*time.Second, "runner registration", func() bool {
		runners := s.Runners()
		return len(runners) == 1 && runners[0].Status == domain.RunnerStatusOnline
	})
	runner := s.Runners()[0]
	runner.OwnerUserID = 42
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultRunner(42, runner.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	putMockDeveloperPlaybook(t, s)
	wf := domain.WorkflowDefinition{
		ID: "runner-gate-e2e", Name: "Runner gate e2e", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{
			ID: "work", Name: "Work", Type: "agent", AgentID: "developer", ExecutorID: "mock",
			MaxAttempts: 1,
		}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "gate routing input", domain.RepositoryContext{}, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "run completion", func() bool {
		current, _ := s.Run(run.ID)
		return current.Status == "succeeded"
	})
	waitFor(t, 10*time.Second, "runner workspace immediate cleanup", func() bool {
		_, statErr := os.Stat(filepath.Join(workspaceRoot, "runs", run.ID))
		current, ok := s.Run(run.ID)
		return ok && os.IsNotExist(statErr) && !current.WorkspaceCleanedAt.IsZero()
	})
	// Both the CLI invocation and the gate command ran on the runner.
	allTasks := s.RunnerTasks()
	tasks := make([]domain.RunnerTask, 0, len(allTasks))
	for _, task := range allTasks {
		if task.Action == "" {
			tasks = append(tasks, task)
		}
	}
	if len(tasks) != 2 || len(allTasks) != 3 {
		t.Fatalf("expected CLI + gate + cleanup tasks, got %d: %+v", len(allTasks), allTasks)
	}
	providers := map[string]bool{}
	for _, task := range tasks {
		providers[task.Provider] = true
		if task.Status != domain.RunnerTaskSuccess {
			t.Fatalf("task %s (%s) status = %s", task.ID, task.Provider, task.Status)
		}
	}
	if !providers["mock"] || !providers["terminal"] {
		t.Fatalf("expected mock + terminal tasks, got %v", providers)
	}
	// The gate command output came back from the runner machine.
	for _, task := range tasks {
		if task.Provider == "terminal" && !strings.Contains(task.Result, "gate-ran-on-runner") {
			t.Fatalf("gate task result missing command output: %q", task.Result)
		}
	}
	cancel()
}

func TestProviderArgvTemplates(t *testing.T) {
	cases := []struct {
		provider string
		binary   string
		args     []string
		wantHead []string
		wantTail string
	}{
		{"claude-code", "/bin/claude", nil, []string{"/bin/claude", "-p", "--output-format", "text"}, "do it"},
		{"codex", "/bin/codex", nil, []string{"/bin/codex", "exec", "--skip-git-repo-check"}, "do it"},
	}
	for _, tc := range cases {
		argv, err := providerArgv(tc.provider, tc.binary, tc.wantTail, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if len(argv) < len(tc.wantHead)+1 {
			t.Fatalf("%s: argv too short: %v", tc.provider, argv)
		}
		for i, want := range tc.wantHead {
			if argv[i] != want {
				t.Fatalf("%s: argv[%d] = %q, want %q (full: %v)", tc.provider, i, argv[i], want, argv)
			}
		}
		if argv[len(argv)-1] != tc.wantTail {
			t.Fatalf("%s: prompt must be last, got %q", tc.provider, argv[len(argv)-1])
		}
	}
	if _, err := providerArgv("unknown", "/bin/x", "p", nil); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestControlFallbackIsFasterThanLeaseRenew(t *testing.T) {
	if controlFallbackInterval > time.Second {
		t.Fatalf("control fallback %s is too slow for pause/cancel", controlFallbackInterval)
	}
	if controlFallbackInterval >= leaseRenewInterval {
		t.Fatalf("control fallback %s must be faster than lease renew %s", controlFallbackInterval, leaseRenewInterval)
	}
}

func TestCloneOutputRedactsToken(t *testing.T) {
	token := "tok-secret-123"
	got := cloneOutput([]byte(strings.Repeat("x", 280)+" https://private:"+token+"@git.example.com/a.git"), token)
	if strings.Contains(got, token) {
		t.Fatalf("clone error leaked the token: %s", got)
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("clone error was not redacted: %s", got)
	}
	if len(got) > 300 {
		t.Fatalf("clone error length = %d, want <= 300", len(got))
	}
}

func TestCleanupDockerWorkspaceSkipsVanishedContainer(t *testing.T) {
	workspace := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "rm.log")
	installFakeDocker(t, `#!/bin/sh
last=""
for a in "$@"; do last="$a"; done
case "$1" in
ps) printf '%s\n' gone keep ;;
inspect)
  case "$last" in
  gone) echo "Error: No such object: gone" >&2; exit 1 ;;
  keep)
    case "$3" in
    *Config.Image*) echo ubuntu ;;
    *) printf '%s\n' "$AF_TEST_WORKSPACE/node" ;;
    esac
    ;;
  *) echo "unexpected $last" >&2; exit 1 ;;
  esac
  ;;
rm) printf '%s\n' "$last" >> "$AF_TEST_RM_LOG" ;;
*) echo "unexpected $1" >&2; exit 1 ;;
esac
`)
	t.Setenv("AF_TEST_WORKSPACE", workspace)
	t.Setenv("AF_TEST_RM_LOG", logPath)
	if err := cleanupDockerWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	removed, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(removed)) != "keep" {
		t.Fatalf("removed = %q, want keep", removed)
	}
}

func TestCleanupDockerWorkspaceReturnsRealInspectFailure(t *testing.T) {
	installFakeDocker(t, `#!/bin/sh
case "$1" in
ps) echo bad ;;
*) echo "permission denied" >&2; exit 1 ;;
esac
`)
	err := cleanupDockerWorkspace(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want permission denied", err)
	}
}

func installFakeDocker(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

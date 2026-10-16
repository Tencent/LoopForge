package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestAcquireRunEnvironmentRefreshesGitTokenWithoutReplacingWorkspace(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 3, LoginName: "example-admin"}); err != nil {
		t.Fatal(err)
	}
	e := leaseEngine(t, s, "worker")
	e.leaseTTL = time.Minute
	run := leaseRun(t, s, "run-refresh-token", "running", "pending")
	owned, err := e.acquireLease(run)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	profile := domain.SandboxProfile{ID: "local", Driver: "local"}
	env, _, err := e.acquireRunEnvironment(ctx, owned, profile, runtime.SandboxRequirements{})
	if err != nil {
		t.Fatal(err)
	}
	workspace := env.Workspace()
	if env.credentials.GitToken != "" {
		t.Fatalf("git token = %q, want empty before the user configures one", env.credentials.GitToken)
	}

	if _, err := s.UpdateUserCredentials(3, repo.UserCredentialUpdate{GitToken: "tok_user"}); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.RunState(run.ID)
	if !ok {
		t.Fatal("run missing")
	}
	stored.ExecutionContext.ResponsibleUserID = 3
	if err := s.PutRun(stored); err != nil {
		t.Fatal(err)
	}
	again, _, err := e.acquireRunEnvironment(ctx, owned, profile, runtime.SandboxRequirements{})
	if err != nil {
		t.Fatal(err)
	}
	if again != env {
		t.Fatal("retry replaced the run environment")
	}
	if again.Workspace() != workspace {
		t.Fatalf("workspace changed from %s to %s", workspace, again.Workspace())
	}
	if again.credentials.GitToken != "tok_user" {
		t.Fatalf("refreshed git token = %q, want tok_user", again.credentials.GitToken)
	}
	out, err := again.Exec(ctx, runtime.CommandSpec{
		Argv:               []string{"sh", "-c", `printf %s "$AGENTFLOW_GIT_TOKEN"`},
		CredentialBindings: []runtime.CredentialBinding{runtime.BindGit},
	}, func(domain.TelemetryEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "tok_user" {
		t.Fatalf("sandbox command token = %q, want tok_user", out)
	}
}

func TestRunEnvironmentPreparesRepositoriesOnce(t *testing.T) {
	env := &runEnvironment{}
	calls := 0
	prepare := func() error {
		calls++
		return nil
	}
	if err := env.prepareRepositoriesOnce(prepare); err != nil {
		t.Fatal(err)
	}
	if err := env.prepareRepositoriesOnce(prepare); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("prepare calls = %d, want 1", calls)
	}
}

func TestRunEnvironmentRetriesRepositoryPreparationAfterFailure(t *testing.T) {
	env := &runEnvironment{}
	calls := 0
	prepare := func() error {
		calls++
		if calls == 1 {
			return errors.New("temporary checkout failure")
		}
		return nil
	}
	if err := env.prepareRepositoriesOnce(prepare); err == nil {
		t.Fatal("first preparation unexpectedly succeeded")
	}
	if err := env.prepareRepositoriesOnce(prepare); err != nil {
		t.Fatalf("retry preparation: %v", err)
	}
	if calls != 2 {
		t.Fatalf("prepare calls = %d, want 2", calls)
	}
}

func TestReconstructedRunEnvironmentCleansStaleAgentGitIndexLock(t *testing.T) {
	workspace := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", workspace}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "agentflow@test.local")
	runGit("config", "user.name", "AgentFlow Test")
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("repository instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "AGENTS.md")
	runGit("commit", "-m", "baseline")

	ctx := context.Background()
	if err := runtime.ProtectProjectAgentGitIndex(ctx, workspace, "docker"); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(workspace, ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// A worker restart reconstructs runEnvironment while preserving the run
	// workspace. The first node on the new worker must revalidate checkout and
	// isolation, remove a stale lock, then leave AGENTS.md protected again.
	reconstructed := &runEnvironment{}
	repository := domain.RepositoryContext{RepositoryID: "repo", BaseBranch: "main", WorkBranch: "main"}
	err := reconstructed.prepareRepositoriesOnce(func() error {
		if err := runtime.RestoreProjectAgentGitIndex(ctx, workspace, "docker"); err != nil {
			return err
		}
		if err := prepareRepository(ctx, workspace, repository); err != nil {
			return err
		}
		return runtime.ProtectProjectAgentGitIndex(ctx, workspace, "docker")
	})
	if err != nil {
		t.Fatalf("reconstruct run environment: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("stale index lock survived reconstruction: %v", err)
	}
	if flag := runGit("ls-files", "-v", "--", "AGENTS.md"); !strings.HasPrefix(flag, "S ") {
		t.Fatalf("AGENTS.md protection after reconstruction = %q", flag)
	}
}

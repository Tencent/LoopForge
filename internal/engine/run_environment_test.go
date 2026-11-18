package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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
	t.Parallel()
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
	if env.credentials.GitHTTPCredentials["github.com"].Token != "" {
		t.Fatalf("git token = %q, want empty before the user configures one", env.credentials.GitHTTPCredentials["github.com"].Token)
	}

	if err := s.SaveGitHTTPCredential(3, "", domain.GitHTTPCredentialSecret{GitHTTPCredential: domain.GitHTTPCredential{Host: "github.com", Name: "GitHub", Platform: domain.GitPlatformGitHub}, Token: "tok_user"}); err != nil {
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
	if again.credentials.GitHTTPCredentials["github.com"].Token != "tok_user" {
		t.Fatalf("refreshed git token = %q, want tok_user", again.credentials.GitHTTPCredentials["github.com"].Token)
	}
	out, err := again.Exec(ctx, runtime.CommandSpec{
		Argv:               []string{"sh", "-c", `test -z "$AGENTFLOW_GIT_TOKEN" && printf 'protocol=https\nhost=github.com\n\n' | git credential fill | python3 -c 'import sys,hashlib; c=dict(l.rstrip().split("=",1) for l in sys.stdin if "=" in l); print(hashlib.sha256(c["password"].encode()).hexdigest())'`},
		CredentialBindings: []runtime.CredentialBinding{runtime.BindGit},
	}, func(domain.TelemetryEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != fmt.Sprintf("%x", sha256.Sum256([]byte("tok_user"))) {
		t.Fatalf("the refreshed token was not available through the Git helper: %q", out)
	}
}

func TestRunEnvironmentPreparesRepositoriesOnce(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	runGit("update-index", "--skip-worktree", "AGENTS.md")
	const legacyExcludes = `# AgentFlow managed sandbox: repository Agent configuration
AGENTS.md
AGENTS.override.md
CLAUDE.md
CLAUDE.local.md
CODEBUDDY.md
.mcp.json
.agents/
.codex/
.claude/
.codebuddy/
`
	if err := os.WriteFile(filepath.Join(workspace, ".git", "info", "exclude"), []byte(legacyExcludes), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(workspace, ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// A worker restart reconstructs runEnvironment while preserving the run
	// workspace. The first node on the new worker must revalidate checkout and
	// make the existing instruction files visible to Git again.
	reconstructed := &runEnvironment{}
	repository := domain.RepositoryContext{RepositoryID: "repo", BaseBranch: "main", WorkBranch: "main"}
	err := reconstructed.prepareRepositoriesOnce(func() error {
		return prepareRepositories(ctx, workspace, []domain.RepositoryContext{repository})
	})
	if err != nil {
		t.Fatalf("reconstruct run environment: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("stale index lock survived reconstruction: %v", err)
	}
	if flag := runGit("ls-files", "-v", "--", "AGENTS.md"); !strings.HasPrefix(flag, "H ") {
		t.Fatalf("AGENTS.md index flag after reconstruction = %q", flag)
	}
}

func TestRunEnvironmentUnwrapsForEnsureSandboxBrowser(t *testing.T) {
	bin := t.TempDir()
	chromium := filepath.Join(bin, "chromium")
	script := `#!/bin/sh
profile=
for arg in "$@"; do
  case "$arg" in --user-data-dir=*) profile="${arg#*=}";; esac
done
mkdir -p "$profile"
printf '9223\n/devtools/browser/env\n' > "$profile/DevToolsActivePort"
exec sleep 30
`
	if err := os.WriteFile(chromium, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "playwright-mcp"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CHROMIUM", chromium)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	workspace := t.TempDir()
	sb, err := runtime.OpenRemoteSandbox(context.Background(), "env-browser", workspace, domain.SandboxProfile{Driver: domain.SandboxDriverLocal}, runtime.SandboxRequirements{}, runtime.SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	env := &runEnvironment{sandbox: sb}
	t.Cleanup(func() {
		_, _ = runtime.HandleBrowser(context.Background(), "run-env-browser", domain.BrowserRequest{Operation: "stop"})
		_ = env.Close(context.Background())
	})

	mcp := runtime.EnsureSandboxBrowser(context.Background(), env, env.Workspace(), "run-env-browser", nil)
	if mcp == nil {
		t.Fatal("EnsureSandboxBrowser returned nil for *runEnvironment wrapping local sandbox")
	}
	if mcp.ID != runtime.BrowserMCPServerID {
		t.Fatalf("EnsureSandboxBrowser ID = %q, want %q", mcp.ID, runtime.BrowserMCPServerID)
	}
}

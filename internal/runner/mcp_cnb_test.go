package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)


func TestExecuteInvocationGitHubMCPOptionalForCNBAndSSHRuns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	assets := domain.AgentAssetBundle{
		MCPServers: []domain.MCPServerDefinition{
			{ID: "github", Name: "GitHub", Transport: "http", AuthType: "github-token", URL: "https://api.githubcopilot.com/mcp/"},
		},
	}

	initCheckout := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitT(t, dir, "init", "-q")
		runGitT(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "init")
	}

	t.Run("CNB-only skips missing github-token MCP", func(t *testing.T) {
		ws := filepath.Join(t.TempDir(), "ws")
		initCheckout(t, filepath.Join(ws, "cnb"))
		d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL), executor: runtime.NewExecutorRunner()}
		task := domain.RunnerTask{
			ID:        "cnb-only",
			Workspace: ws,
			Prompt:    "ok",
			Invocation: &domain.RunnerInvocation{
				InvocationID: "inv-cnb-only",
				RunID:        "run-cnb-only",
				Executor:     domain.ExecutorDefinition{Kind: "mock"},
				Agent:        domain.AgentDefinition{ID: "dev", Name: "Dev"},
				Assets:       assets,
				Repositories: []domain.RunnerRepository{{RepositoryID: "cnb", Dir: "cnb", CloneURL: "https://cnb.cool/org/repo.git"}},
			},
		}
		res, _ := d.executeInvocation(context.Background(), task)
		if res.status != domain.RunnerTaskSuccess {
			t.Fatalf("expected success for CNB-only run, got %+v", res)
		}
	})

	t.Run("CNB plus SSH skips missing github-token MCP", func(t *testing.T) {
		ws := filepath.Join(t.TempDir(), "ws")
		initCheckout(t, filepath.Join(ws, "cnb"))
		initCheckout(t, filepath.Join(ws, "ssh"))
		d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL), executor: runtime.NewExecutorRunner()}
		task := domain.RunnerTask{
			ID:        "cnb-ssh",
			Workspace: ws,
			Prompt:    "ok",
			Invocation: &domain.RunnerInvocation{
				InvocationID: "inv-cnb-ssh",
				RunID:        "run-cnb-ssh",
				Executor:     domain.ExecutorDefinition{Kind: "mock"},
				Agent:        domain.AgentDefinition{ID: "dev", Name: "Dev"},
				Assets:       assets,
				Repositories: []domain.RunnerRepository{
					{RepositoryID: "cnb", Dir: "cnb", CloneURL: "https://cnb.cool/org/repo.git"},
					{RepositoryID: "ssh", Dir: "ssh", CloneURL: "git@example.com:org/infra.git", UseSSH: true},
				},
			},
		}
		res, _ := d.executeInvocation(context.Background(), task)
		if res.status != domain.RunnerTaskSuccess {
			t.Fatalf("expected success for CNB+SSH run, got %+v", res)
		}
	})

	t.Run("legacy single Repository CNB skips missing github-token MCP", func(t *testing.T) {
		ws := filepath.Join(t.TempDir(), "ws")
		initCheckout(t, ws)
		d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL), executor: runtime.NewExecutorRunner()}
		task := domain.RunnerTask{
			ID:        "legacy-cnb",
			Workspace: ws,
			Prompt:    "ok",
			Invocation: &domain.RunnerInvocation{
				InvocationID: "inv-legacy-cnb",
				RunID:        "run-legacy-cnb",
				Executor:     domain.ExecutorDefinition{Kind: "mock"},
				Agent:        domain.AgentDefinition{ID: "dev", Name: "Dev"},
				Assets:       assets,
				Repository:   &domain.RunnerRepository{RepositoryID: "cnb", CloneURL: "https://cnb.cool/org/repo.git"},
			},
		}
		res, _ := d.executeInvocation(context.Background(), task)
		if res.status != domain.RunnerTaskSuccess {
			t.Fatalf("expected success for legacy CNB run, got %+v", res)
		}
	})

	t.Run("mixed CNB and GitHub fails without GitToken", func(t *testing.T) {
		ws := filepath.Join(t.TempDir(), "ws")
		initCheckout(t, filepath.Join(ws, "cnb"))
		initCheckout(t, filepath.Join(ws, "gh"))
		d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL), executor: runtime.NewExecutorRunner()}
		task := domain.RunnerTask{
			ID:        "cnb-gh",
			Workspace: ws,
			Prompt:    "ok",
			Invocation: &domain.RunnerInvocation{
				InvocationID: "inv-cnb-gh",
				RunID:        "run-cnb-gh",
				Executor:     domain.ExecutorDefinition{Kind: "mock"},
				Agent:        domain.AgentDefinition{ID: "dev", Name: "Dev"},
				Assets:       assets,
				Repositories: []domain.RunnerRepository{
					{RepositoryID: "cnb", Dir: "cnb", CloneURL: "https://cnb.cool/org/repo.git"},
					{RepositoryID: "gh", Dir: "gh", CloneURL: "https://github.com/org/repo.git"},
				},
			},
		}
		res, _ := d.executeInvocation(context.Background(), task)
		if res.status != domain.RunnerTaskFailed || !strings.Contains(res.errMsg, "bind MCP credentials") {
			t.Fatalf("expected MCP credentials failure for mixed CNB+GitHub run, got %+v", res)
		}
	})

	t.Run("zero repositories fails without GitToken", func(t *testing.T) {
		ws := filepath.Join(t.TempDir(), "ws")
		d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL), executor: runtime.NewExecutorRunner()}
		task := domain.RunnerTask{
			ID:        "no-repos",
			Workspace: ws,
			Prompt:    "ok",
			Invocation: &domain.RunnerInvocation{
				InvocationID: "inv-no-repos",
				RunID:        "run-no-repos",
				Executor:     domain.ExecutorDefinition{Kind: "mock"},
				Agent:        domain.AgentDefinition{ID: "dev", Name: "Dev"},
				Assets:       assets,
			},
		}
		res, _ := d.executeInvocation(context.Background(), task)
		if res.status != domain.RunnerTaskFailed || !strings.Contains(res.errMsg, "bind MCP credentials") {
			t.Fatalf("expected MCP credentials failure for 0-repo run, got %+v", res)
		}
	})
}

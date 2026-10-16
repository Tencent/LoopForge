package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// seedIntegrationBranch adds a commit touching path on a new integration
// branch of the bare remote, standing in for work another issue merged.
func seedIntegrationBranch(t *testing.T, remote, branch, path, content string) {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "seed")
	runGitT(t, filepath.Dir(clone), "clone", "-q", remote, clone)
	runGitT(t, clone, "checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(clone, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, clone, "add", path)
	runGitT(t, clone, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "other issue")
	runGitT(t, clone, "push", "-q", "origin", branch)
}

// 定时 SOP 跑在执行机器上时，合入集成分支要在执行机器的工作区里做，推上去的才是真正的改动。
func TestRunnerModeScheduledRunIntegratesOnRunner(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	remote := bareRemoteWithBaseline(t)
	seedIntegrationBranch(t, remote, "integration", "other.txt", "from another issue\n")
	runner := startReportingRunner(t, s, server.URL)
	if !runner.SupportsIntegrate() {
		t.Fatalf("runner version %q does not integrate", runner.RunnerVersion)
	}
	wf := domain.WorkflowDefinition{
		ID: "runner-integrate", Name: "Runner integrate", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "implement", Name: "实现", Type: "script", ScriptLanguage: "shell", ExecutionScope: domain.ExecutionScopeRepositories, Script: "printf 'fixed\\n' > feature.txt && printf 'report\\n' > fix-report.md", MaxAttempts: 1},
		},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "issue work", domain.RepositoryContext{RepositoryID: "repo", Name: "svc", CloneURL: remote, BaseBranch: "master", WorkBranch: "fix/issue-1", IntegrateBranch: "integration"}, domain.TelemetryContext{ResponsibleUserID: 42, ScheduleJobID: "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "scheduled runner run", func() bool {
		current, ok := s.Run(run.ID)
		return ok && (current.Status == "succeeded" || current.Status == "failed")
	})
	final, _ := s.Run(run.ID)
	if final.Status != "succeeded" {
		t.Fatalf("status = %s, implement error = %q, events = %+v", final.Status, final.Nodes["implement"].Error, final.Events)
	}
	files := runGitT(t, remote, "ls-tree", "-r", "--name-only", "refs/heads/integration")
	for _, want := range []string{"feature.txt", "other.txt", "README.md"} {
		if !strings.Contains(files, want) {
			t.Fatalf("integration branch misses %s: %s", want, files)
		}
	}
	if strings.Contains(files, "fix-report.md") {
		t.Fatalf("SOP report reached the integration branch: %s", files)
	}
	work := runGitT(t, remote, "rev-parse", "refs/heads/fix/issue-1")
	if integration := runGitT(t, remote, "rev-parse", "refs/heads/integration"); integration != work {
		t.Fatalf("integration %s != work branch %s after direct push", integration, work)
	}
	var integrateTasks int
	for _, task := range s.RunnerTasks() {
		if task.RunID == run.ID && task.Action == domain.RunnerTaskActionIntegrate {
			integrateTasks++
			if task.Status != domain.RunnerTaskSuccess {
				t.Fatalf("integrate task %s ended %s: %s", task.ID, task.Status, task.Error)
			}
		}
	}
	if integrateTasks != 2 {
		t.Fatalf("integrate tasks = %d, want merge + direct push", integrateTasks)
	}
}

func TestIntegrateTaskRepositoriesReportsConflictAndKeepsMerge(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	seedIntegrationBranch(t, remote, "integration", "README.md", "their baseline\n")
	workspace := t.TempDir()
	checkout := filepath.Join(workspace, "svc")
	runGitT(t, workspace, "clone", "-q", remote, checkout)
	runGitT(t, checkout, "checkout", "-q", "-b", "fix/issue-2")
	runGitT(t, checkout, "config", "user.name", "t")
	runGitT(t, checkout, "config", "user.email", "t@t")
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("our change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inv := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{RepositoryID: "repo", Name: "svc", Dir: "svc", WorkBranch: "fix/issue-2", IntegrateBranch: "integration", PushIntegrate: true}}}
	results, err := integrateTaskRepositories(context.Background(), workspace, inv, "issue #2")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || strings.Join(results[0].Conflicts, ",") != "README.md" || results[0].PushedIntegrate {
		t.Fatalf("results = %+v", results)
	}
	runGitT(t, checkout, "rev-parse", "--verify", "MERGE_HEAD")
	if out := runGitT(t, remote, "branch", "--list", "fix/issue-2"); out != "" {
		t.Fatalf("conflicted work branch was pushed: %s", out)
	}
}

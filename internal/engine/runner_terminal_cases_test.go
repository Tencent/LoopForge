package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func cvEFinishQueuedTask(t *testing.T, e *Engine, status, result, errText string, exitCode *int) {
	t.Helper()
	tasks := e.repo.RunnerTasks()
	if len(tasks) == 0 {
		t.Fatal("runner dispatch did not create a task")
	}
	task := tasks[0]
	task.Status = status
	task.Result = result
	task.Error = errText
	task.ExitCode = exitCode
	task.Attempt = 1
	task.UpdatedAt = time.Now().UTC()
	payload, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.repo.DB.Table("runner_tasks").Where("id = ?", task.ID).Updates(map[string]any{
		"status": status, "payload": payload, "updated_at": task.UpdatedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func cvETerminalEngine(t *testing.T, status, result, errText string, exitCode *int) (*Engine, domain.Runner) {
	t.Helper()
	e, _ := cvEEngine(t)
	runner := cvERunner(t, e, "runner-terminal", domain.RunnerStatusOnline, "terminal", "codex")
	e.SetRunnerDispatchNotify(func(string) {
		cvEFinishQueuedTask(t, e, status, result, errText, exitCode)
	})
	return e, runner
}

func TestRunnerTerminalDispatchResults(t *testing.T) {
	for _, tc := range []struct {
		name, status, errText string
		exit                  *int
		wantErr               string
	}{
		{name: "success", status: domain.RunnerTaskSuccess},
		{name: "failed-default", status: domain.RunnerTaskFailed, wantErr: "runner task failed"},
		{name: "failed-exit", status: domain.RunnerTaskFailed, errText: "bad command", exit: func() *int { v := 17; return &v }(), wantErr: "exit 17"},
		{name: "canceled", status: domain.RunnerTaskCanceled, wantErr: "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := cvETerminalEngine(t, tc.status, "finished", tc.errText, tc.exit)
			sb := &runEnvironment{sandbox: &cvESandbox{id: "sb", workspace: t.TempDir()}}
			run := cvERunnerRun("dispatch-"+tc.name, "runner-terminal")
			node := domain.WorkflowNode{ID: "node", Type: "agent"}
			ex := domain.ExecutorDefinition{Kind: "codex"}
			got, err := e.dispatchRunnerTask(context.Background(), "runner-terminal", run, node, sb, sb.Workspace(), ex, domain.AgentDefinition{}, domain.AgentAssetBundle{}, "prompt", "inv", runtime.InvocationOptions{}, func(domain.TelemetryEvent) {})
			if tc.wantErr == "" {
				if err != nil || got != "finished" {
					t.Fatalf("result=%q err=%v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRunnerScriptAndCommandTerminalResults(t *testing.T) {
	for _, status := range []string{domain.RunnerTaskSuccess, domain.RunnerTaskFailed, domain.RunnerTaskCanceled} {
		t.Run("script-"+status, func(t *testing.T) {
			e, _ := cvETerminalEngine(t, status, "script-output", "", nil)
			sb := &runEnvironment{sandbox: &cvESandbox{id: "sb", workspace: t.TempDir()}}
			run := cvERunnerRun("script-"+status, "runner-terminal")
			got, err := e.dispatchRunnerScript(context.Background(), "runner-terminal", run, "node", sb.Workspace(), "python", "print(1)", "", nil, sb, "", "", "", domain.AgentAssetBundle{}, func(domain.TelemetryEvent) {})
			if status == domain.RunnerTaskSuccess {
				if err != nil || got != "script-output" {
					t.Fatalf("result=%q err=%v", got, err)
				}
			} else if err == nil {
				t.Fatalf("status %q unexpectedly succeeded", status)
			}
		})
		t.Run("command-"+status, func(t *testing.T) {
			e, _ := cvETerminalEngine(t, status, "command-output", "", nil)
			got, err := e.dispatchRunnerCommand(context.Background(), "runner-terminal", "run-command", "gate", "/workspace", "true", func(domain.TelemetryEvent) {})
			if status == domain.RunnerTaskSuccess {
				if err != nil || got != "command-output" {
					t.Fatalf("result=%q err=%v", got, err)
				}
			} else if err == nil {
				t.Fatalf("status %q unexpectedly succeeded", status)
			}
		})
	}
}

func TestRunnerIntegrateAndConversationTerminalResults(t *testing.T) {
	rc := domain.RepositoryContext{RepositoryID: "repo", Name: "Repo", CloneURL: "https://example.invalid/repo.git", WorkspaceDir: "repo", BaseBranch: "main", WorkBranch: "work", IntegrateBranch: "integration"}
	run := cvERunnerRun("terminal-integrate", "runner-terminal")
	run.Repositories = []domain.RepositoryContext{rc}
	run.ExecutionContext.RunnerWorkspaceLayout = domain.RunnerWorkspaceLayoutShared

	integrationJSON := `[{"repositoryId":"repo","name":"Repo","workBranch":"work","integrateBranch":"integration","head":"abc","conflicts":["one.go"],"pushedIntegrate":true}]`
	e, runner := cvETerminalEngine(t, domain.RunnerTaskSuccess, integrationJSON, "", nil)
	conflicts, err := e.integrateOnRunner(context.Background(), runner, run, rc, true)
	if err != nil || len(conflicts) != 1 || conflicts[0] != "one.go" {
		t.Fatalf("conflicts=%v err=%v", conflicts, err)
	}

	e, runner = cvETerminalEngine(t, domain.RunnerTaskSuccess, "{", "", nil)
	if _, err := e.integrateOnRunner(context.Background(), runner, run, rc, false); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("invalid integrate result err=%v", err)
	}
	for _, tc := range []struct{ status, errText, want string }{
		{domain.RunnerTaskCanceled, "", "canceled"},
		{domain.RunnerTaskFailed, "", "failed on runner"},
		{domain.RunnerTaskFailed, "integration broke", "integration broke"},
	} {
		e, runner = cvETerminalEngine(t, tc.status, "", tc.errText, nil)
		if _, err := e.integrateOnRunner(context.Background(), runner, run, rc, false); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("integrate status=%s err=%v", tc.status, err)
		}
	}

	headsJSON := `[{"repositoryId":"repo","name":"Repo","workBranch":"work","head":"def"}]`
	e, _ = cvETerminalEngine(t, domain.RunnerTaskSuccess, headsJSON, "", nil)
	heads, err := e.pushConversationOnRunner(context.Background(), "runner-terminal", run, []domain.RepositoryContext{rc}, 0, "token", "User", "user@example.com", "message", "")
	if err != nil || len(heads) != 1 {
		t.Fatalf("heads=%v err=%v", heads, err)
	}
	e, _ = cvETerminalEngine(t, domain.RunnerTaskSuccess, "{", "", nil)
	if _, err := e.pushConversationOnRunner(context.Background(), "runner-terminal", run, []domain.RepositoryContext{rc}, 0, "", "", "", "message", ""); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("invalid push result err=%v", err)
	}
	e, _ = cvETerminalEngine(t, domain.RunnerTaskFailed, "", "", nil)
	if _, err := e.pushConversationOnRunner(context.Background(), "runner-terminal", run, []domain.RepositoryContext{rc}, 0, "", "", "", "message", ""); err == nil {
		t.Fatal("failed push succeeded")
	}
}

func TestRunnerConversationPushBindsRequesterGitLabPATToSelectedRepositories(t *testing.T) {
	const requesterID int64 = 42
	gitLab := domain.RepositoryContext{RepositoryID: "gitlab", Provider: domain.RepositoryProviderGitLab, Name: "GitLab", CloneURL: "https://gitlab.example.test/team/repo.git", WorkspaceDir: "gitlab", BaseBranch: "main", WorkBranch: "work"}
	github := domain.RepositoryContext{RepositoryID: "github", Provider: "github", Name: "GitHub", CloneURL: "https://github.example.test/team/repo.git", WorkspaceDir: "github", BaseBranch: "main", WorkBranch: "work"}
	for _, tc := range []struct {
		name         string
		repositories []domain.RepositoryContext
		selected     domain.RepositoryContext
		wantUserID   int64
	}{
		{name: "selected GitLab repository", repositories: []domain.RepositoryContext{gitLab}, selected: gitLab, wantUserID: requesterID},
		{name: "unselected GitLab repository", repositories: []domain.RepositoryContext{gitLab, github}, selected: github},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := cvEEngine(t)
			runner := cvERunner(t, e, "runner-terminal", domain.RunnerStatusOnline, "terminal", "codex")
			runner.SupportsGitLabPAT = true
			if err := e.repo.PutRunner(runner); err != nil {
				t.Fatal(err)
			}
			run := cvERunnerRun("terminal-gitlab-push", runner.ID)
			run.ExecutionContext.ResponsibleUserID = 7
			run.Repositories = tc.repositories
			headsJSON := `[{"repositoryId":"` + tc.selected.RepositoryID + `","name":"Repo","workBranch":"work","head":"def"}]`
			dispatched := false
			e.SetRunnerDispatchNotify(func(string) {
				tasks := e.repo.RunnerTasks()
				if len(tasks) != 1 || tasks[0].Invocation == nil {
					t.Fatal("conversation push did not enqueue an invocation")
				}
				invocation := tasks[0].Invocation
				if invocation.GitLabUserID != tc.wantUserID || invocation.GitLabToken != "" || invocation.GitSSHUserID != 0 {
					t.Fatalf("queued credential ownership = user %d, token %q, ssh user %d; want user %d", invocation.GitLabUserID, invocation.GitLabToken, invocation.GitSSHUserID, tc.wantUserID)
				}
				dispatched = true
				cvEFinishQueuedTask(t, e, domain.RunnerTaskSuccess, headsJSON, "", nil)
			})
			heads, err := e.pushConversationOnRunner(context.Background(), runner.ID, run, []domain.RepositoryContext{tc.selected}, requesterID, "token", "User", "user@example.com", "message", "")
			if err != nil || len(heads) != 1 || !dispatched {
				t.Fatalf("heads=%v dispatched=%t err=%v", heads, dispatched, err)
			}
		})
	}
}

func TestRunnerAlignTerminalResults(t *testing.T) {
	repository := domain.RepositoryContext{
		RepositoryID: "align-repo", Name: "Align Repo", CloneURL: "https://example.invalid/align.git",
		DefaultBranch: "main", IntegrateBranch: "integration",
	}
	for _, tc := range []struct {
		name, status, result, errText, want string
	}{
		{name: "success", status: domain.RunnerTaskSuccess, result: `{}`},
		{name: "invalid-json", status: domain.RunnerTaskSuccess, result: `{`, want: "decode align result"},
		{name: "conflicts", status: domain.RunnerTaskSuccess, result: `{"conflicts":["a.go"],"blocks":[{"file":"a.go"}]}`, want: "冲突"},
		{name: "failed-default", status: domain.RunnerTaskFailed, want: "align_baseline failed on runner"},
		{name: "failed-message", status: domain.RunnerTaskFailed, errText: "runner alignment broke", want: "runner alignment broke"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := cvETerminalEngine(t, tc.status, tc.result, tc.errText, nil)
			runner, ok := e.repo.Runner("runner-terminal")
			if !ok {
				t.Fatal("runner missing")
			}
			runner.Labels = []string{"align-pool"}
			runner.RunnerVersion = domain.RunnerVersionWorkspaceOwnerPreparation
			if err := e.repo.PutRunner(runner); err != nil {
				t.Fatal(err)
			}
			e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "align-pool", Projects: []string{"*"}})
			err := e.AlignBaseline(repository, "token")
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

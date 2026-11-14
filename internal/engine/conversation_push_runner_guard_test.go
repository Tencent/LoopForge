package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// pendingConversationPushEvents returns the queued work-branch telemetry for runID.
func pendingConversationPushEvents(e *Engine, runID string) []domain.TelemetryEvent {
	e.obsMu.Lock()
	defer e.obsMu.Unlock()
	var events []domain.TelemetryEvent
	for _, pending := range e.obsPending {
		if pending.runID == runID && strings.HasPrefix(pending.event.Name, "work_branch.") {
			events = append(events, pending.event)
		}
	}
	return events
}

func conversationPushRunnerFixture(t *testing.T, runID, status, result, errText string) (*Engine, domain.Run, domain.RepositoryContext, *domain.RunnerTask) {
	t.Helper()
	e, _ := cvEEngine(t)
	runner := domain.Runner{ID: "runner-push", Name: "runner-push", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionSharedPush,
		Runtimes: []domain.RunnerRuntime{{Provider: "terminal", Status: "ready", AuthMode: domain.RuntimeAuthLocal}}}
	if err := e.repo.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	selected := domain.RepositoryContext{RepositoryID: "repo-a", Name: "A", CloneURL: "https://example.invalid/a.git", WorkspaceDir: "a", BaseBranch: "main", WorkBranch: "work-a"}
	other := domain.RepositoryContext{RepositoryID: "repo-b", Name: "B", CloneURL: "https://example.invalid/b.git", WorkspaceDir: "b", BaseBranch: "main", WorkBranch: "work-b"}
	run := cvERunnerRun(runID, runner.ID)
	run.Status = "running"
	run.Repositories = []domain.RepositoryContext{selected, other}
	now := time.Now().UTC()
	run.CreatedAt, run.UpdatedAt = now, now
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	dispatched := &domain.RunnerTask{}
	e.SetRunnerDispatchNotify(func(string) {
		if tasks := e.repo.RunnerTasks(); len(tasks) > 0 {
			*dispatched = tasks[0]
		}
		cvEFinishQueuedTask(t, e, status, result, errText, nil)
	})
	return e, run, selected, dispatched
}

// The runner push must only sync the repositories the caller selected and
// carry the shared push target into the dispatched invocation; the pushed
// heads are recorded as telemetry attributed to the runner.
func TestPushConversationOnRunnerDispatchesSelectedRepositoriesAndObservesHeads(t *testing.T) {
	headsJSON := `[{"repositoryId":"repo-a","name":"A","workBranch":"work-a","head":"abc","sharedBranch":"preview","sharedHead":"def"}]`
	e, run, selected, dispatched := conversationPushRunnerFixture(t, "runner-push-ok", domain.RunnerTaskSuccess, headsJSON, "")

	heads, err := e.pushConversationOnRunner(context.Background(), "runner-push", run, []domain.RepositoryContext{selected}, 0, "token", "User", "user@example.com", "message", "preview")
	if err != nil || len(heads) != 1 || heads[0].Head != "abc" || heads[0].SharedHead != "def" {
		t.Fatalf("heads=%+v err=%v", heads, err)
	}

	if dispatched.Invocation == nil {
		t.Fatal("push task was not dispatched with an invocation")
	}
	if dispatched.Action != domain.RunnerTaskActionPushWorkBranch || dispatched.WorkBranchMessage != "message" {
		t.Fatalf("task action=%q message=%q", dispatched.Action, dispatched.WorkBranchMessage)
	}
	repos := dispatched.Invocation.Repositories
	if len(repos) != 1 || repos[0].RepositoryID != "repo-a" || repos[0].Dir != "a" {
		t.Fatalf("invocation repositories = %+v, want only the selected repo-a/a", repos)
	}
	if !repos[0].SyncWorkBranch || repos[0].SharedPushTarget != "preview" {
		t.Fatalf("selected repository not marked for shared push: %+v", repos[0])
	}
	if dispatched.Invocation.Repository != nil {
		t.Fatalf("single repository binding must be cleared, got %+v", dispatched.Invocation.Repository)
	}

	events := pendingConversationPushEvents(e, run.ID)
	names := map[string]domain.TelemetryEvent{}
	for _, event := range events {
		names[event.Name] = event
	}
	pushed, ok := names["work_branch.pushed"]
	if !ok || pushed.Attributes["runnerId"] != "runner-push" || pushed.Attributes["commit"] != "abc" {
		t.Fatalf("work_branch.pushed telemetry = %+v (all: %+v)", pushed, events)
	}
	shared, ok := names["work_branch.shared_pushed"]
	if !ok || shared.Attributes["runnerId"] != "runner-push" || shared.Attributes["sharedBranch"] != "preview" {
		t.Fatalf("work_branch.shared_pushed telemetry = %+v (all: %+v)", shared, events)
	}
}

// A runner push rejected for authentication is explained as a Git token
// problem, and no pushed-head telemetry is recorded for the failed task.
func TestPushConversationOnRunnerExplainsAuthFailureWithoutTelemetry(t *testing.T) {
	e, run, selected, _ := conversationPushRunnerFixture(t, "runner-push-auth", domain.RunnerTaskFailed, "",
		"git push origin work-a: fatal: Authentication failed for 'https://example.invalid/a.git/'")

	_, err := e.pushConversationOnRunner(context.Background(), "runner-push", run, []domain.RepositoryContext{selected}, 0, "token", "User", "user@example.com", "message", "")
	if err == nil || !strings.Contains(err.Error(), "Git Token") || !strings.Contains(err.Error(), "Authentication failed") {
		t.Fatalf("err = %v, want Git token explanation wrapping the runner error", err)
	}
	if events := pendingConversationPushEvents(e, run.ID); len(events) != 0 {
		t.Fatalf("failed push recorded telemetry: %+v", events)
	}
}

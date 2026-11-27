package engine

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// A node that runs on the responsible user's machine with that machine's own
// Codex login sends no shared key, so the role's model is kept and the run
// does not claim it used the shared key. On the same machine in personal mode
// the shared key is what runs the node, so the model is still pinned.
func TestSponsoredModelPinSkipsRunnerLocalLogin(t *testing.T) {
	for _, tc := range []struct {
		name      string
		authMode  string
		wantModel string
		sponsored bool
	}{
		{"local login keeps the role model", domain.RuntimeAuthLocal, "gpt-role-model", false},
		{"personal mode runs on the shared key", domain.RuntimeAuthPersonal, "gpt-shared-only", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			if err := st.PutUser(domain.User{ID: 7, LoginName: "member"}); err != nil {
				t.Fatal(err)
			}
			if err := st.SetPlatformModelKey(repo.PlatformModelKeyUpdate{Provider: domain.SharedModelProviderCodex, APIKey: "sk_shared", Models: []string{"gpt-shared-only"}}, 7); err != nil {
				t.Fatal(err)
			}
			runner := domain.Runner{ID: "member-runner", Name: "member-runner", OwnerUserID: 7, Status: domain.RunnerStatusOnline,
				RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation,
				Runtimes:      []domain.RunnerRuntime{{Provider: "codex", Status: "ready", AuthMode: tc.authMode}}}
			if err := st.PutRunner(runner); err != nil {
				t.Fatal(err)
			}
			if err := st.PutAgent(domain.AgentDefinition{ID: "coder", Name: "Coder", ExecutorID: "codex", Model: "gpt-role-model", Prompt: "work"}); err != nil {
				t.Fatal(err)
			}
			wf := domain.WorkflowDefinition{ID: "runner-login", Name: "Runner login", SandboxProfileID: "test-native",
				Nodes: []domain.WorkflowNode{{ID: "work", Name: "Work", Type: "agent", AgentID: "coder", MaxAttempts: 1}}}
			if err := st.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			e := New(st, runtime.NewManager(t.TempDir()))
			queued := make(chan domain.RunnerTask, 4)
			e.SetRunnerDispatchNotify(func(string) {
				for _, task := range st.RunnerTasks() {
					if task.NodeID == "work" && task.Status == domain.RunnerTaskQueued && task.Invocation != nil {
						select {
						case queued <- task:
						default:
						}
					}
				}
			})
			run, err := e.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{}, domain.TelemetryContext{
				ResponsibleUserID: 7, ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runner.ID, ExecutionPinned: true,
				RunnerWorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = e.Cancel(run.ID) })
			var task domain.RunnerTask
			select {
			case task = <-queued:
			case <-time.After(10 * time.Second):
				current, _ := st.RunState(run.ID)
				t.Fatalf("node was not dispatched to the runner: status=%s events=%+v", current.Status, current.Events)
			}
			inv := task.Invocation
			if inv.Executor.Model != tc.wantModel {
				t.Fatalf("dispatched model = %q (credential source %q), want %q", inv.Executor.Model, inv.CredentialSource, tc.wantModel)
			}
			if sent := inv.CodexAPIKey != ""; sent != tc.sponsored {
				t.Fatalf("shared key sent = %v, want %v (credential source %q)", sent, tc.sponsored, inv.CredentialSource)
			}
			current, _ := st.RunState(run.ID)
			noted := false
			for _, event := range current.Events {
				if event.Type == sponsoredRunEvent || strings.Contains(event.Message, "共享 Key") {
					noted = true
				}
			}
			if noted != tc.sponsored {
				t.Fatalf("shared-key note on the run = %v, want %v: %+v", noted, tc.sponsored, current.Events)
			}
		})
	}
}

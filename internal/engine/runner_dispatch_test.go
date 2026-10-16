package engine

import (
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestResolveRunnerResumeSessionIDPrefersEngineOption(t *testing.T) {
	run := domain.Run{Nodes: map[string]*domain.NodeRun{
		"review": {NodeID: "review", ResumeSessionID: "node-session", SessionID: "observed-session"},
	}}
	// The missing-submit recovery path puts the just-observed SessionID into
	// options while nr.ResumeSessionID may already be cleared by telemetry.
	got := resolveRunnerResumeSessionID(run, "review", runtime.InvocationOptions{ResumeSessionID: "recovery-session"})
	if got != "recovery-session" {
		t.Fatalf("engine option must win, got %q", got)
	}
	// Without an option, the durable node field is the fallback.
	got = resolveRunnerResumeSessionID(run, "review", runtime.InvocationOptions{})
	if got != "node-session" {
		t.Fatalf("node field fallback, got %q", got)
	}
	// Unknown node and no option → empty (fresh session).
	got = resolveRunnerResumeSessionID(run, "missing", runtime.InvocationOptions{})
	if got != "" {
		t.Fatalf("expected empty resume id, got %q", got)
	}
	// Blank option must not shadow a usable node field.
	got = resolveRunnerResumeSessionID(run, "review", runtime.InvocationOptions{ResumeSessionID: "   "})
	if got != "node-session" {
		t.Fatalf("blank option should fall through to node field, got %q", got)
	}
}

func TestGateCommandInvocationCarriesResponsibleGitToken(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 3, LoginName: "private", GitToken: "tok_gate"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: "run-gate",
		ExecutionContext: domain.TelemetryContext{
			ProjectID:         "skillhub",
			ResponsibleUserID: 3,
		},
		Repositories: []domain.RepositoryContext{{
			RepositoryID: "skillhub",
			Name:         "skillhub",
			CloneURL:     "https://git.example.com/example/team-project.git",
			BaseBranch:   "agentflow/integration",
			WorkBranch:   "feature/fix-issue-67",
			WorkspaceDir: "skillhub",
		}},
	}
	invocation := (&Engine{repo: s}).gateCommandInvocation(run, "fix")
	if invocation == nil || len(invocation.Repositories) != 1 {
		t.Fatalf("invocation = %+v", invocation)
	}
	got := invocation.Repositories[0]
	if got.GitToken != "tok_gate" || got.Dir != "skillhub" || got.WorkBranch != "feature/fix-issue-67" {
		t.Fatalf("repository = %+v", got)
	}
	if invocation.GitUserName != "private" || invocation.GitUserEmail != "private@users.noreply.loopforge.invalid" {
		t.Fatalf("git identity = %q <%s>", invocation.GitUserName, invocation.GitUserEmail)
	}
}

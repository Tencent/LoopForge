package engine

import (
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

func TestAppendArtifactContractDescribesRunLayout(t *testing.T) {
	got := appendArtifactContract("base", domain.WorkflowNode{
		ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "fix-plan.md", MediaType: "text/markdown", Required: true}},
	}, []domain.RepositoryContext{{Name: "gateway", WorkspaceDir: "gateway"}})
	if !strings.Contains(got, "`./gateway`: gateway") {
		t.Fatalf("prompt must name the checkout directory: %s", got)
	}
	if !strings.Contains(got, "run workspace root") || !strings.Contains(got, "`fix-plan.md`") {
		t.Fatalf("prompt must keep deliverables at the run root: %s", got)
	}
	legacy := appendArtifactContract("base", domain.WorkflowNode{
		ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "fix-plan.md", Required: true}},
	}, []domain.RepositoryContext{{Name: "gateway"}})
	if strings.Contains(legacy, "# Workspace layout") {
		t.Fatalf("legacy root checkout must not invent a subdirectory: %s", legacy)
	}
}

func TestGateExecutionScopeIsExplicit(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"},
		{"config", "user.email", "scope-test@example.com"},
		{"config", "user.name", "Scope Test"},
		{"commit", "--allow-empty", "-qm", "baseline"},
	} {
		cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	putGateTestAgent(t, s, "scope-validator", []domain.VerificationGate{
		{ID: "plan", Name: "plan", Mode: "command", Required: true, ExecutionScope: domain.ExecutionScopeRun, Commands: []string{`test -f fix-plan.md && test ! -e .git`}},
		{ID: "git", Name: "git", Mode: "command", Required: true, ExecutionScope: domain.ExecutionScopeRepositories, Commands: []string{`git rev-parse --is-inside-work-tree`}},
	})
	wf := domain.WorkflowDefinition{
		ID: "explicit-gate-scope", Name: "explicit gate scope", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{
				ID: "mark", Name: "Mark", Type: "script", ScriptLanguage: "shell",
				ExecutionScope: domain.ExecutionScopeRun,
				Script:         `printf 'plan\n' > fix-plan.md && git -C "$AGENTFLOW_WORKSPACE/source" rev-parse --is-inside-work-tree > repo-ok.txt`,
				ArtifactOutputs: []domain.ArtifactContract{
					{Name: "plan", Path: "fix-plan.md", Required: true},
					{Name: "repo-ok", Path: "repo-ok.txt", Required: true},
				},
				MaxAttempts: 1,
			},
			{ID: "validate", Name: "Validate", Type: "agent", AgentID: "scope-validator", ExecutorID: "mock", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "mark-validate", Source: "mark", Target: "validate", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	run, err := e.StartWithContext(wf.ID, "", domain.RepositoryContext{RepositoryID: "source", Name: "source", LocalPath: source, BaseBranch: "master"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := s.Run(run.ID)
		if ok && (current.Status == "succeeded" || current.Status == "failed") {
			run = current
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Status != "succeeded" {
		t.Fatalf("run status = %s node=%+v", run.Status, run.Nodes["mark"])
	}
}

func TestRunScopedGitGateDoesNotEnterCheckout(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"},
		{"config", "user.email", "scope-test@example.com"},
		{"config", "user.name", "Scope Test"},
		{"commit", "--allow-empty", "-qm", "baseline"},
	} {
		cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	putGateTestAgent(t, s, "run-git-validator", []domain.VerificationGate{{
		ID: "git", Name: "git", Mode: "command", Required: true,
		ExecutionScope: domain.ExecutionScopeRun,
		Commands:       []string{`git rev-parse --is-inside-work-tree`},
	}})
	wf := domain.WorkflowDefinition{
		ID: "run-git-gate", Name: "run git gate", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "prepare", Name: "Prepare", Type: "script", ScriptLanguage: "shell", Script: "printf 'ok\\n' > note.md", MaxAttempts: 1},
			{ID: "check", Name: "Check", Type: "agent", AgentID: "run-git-validator", ExecutorID: "mock", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "prepare-check", Source: "prepare", Target: "check", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	run, err := e.StartWithContext(wf.ID, "", domain.RepositoryContext{RepositoryID: "source", Name: "source", LocalPath: source, BaseBranch: "master"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := s.Run(run.ID)
		if ok && (current.Status == "succeeded" || current.Status == "failed") {
			run = current
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Status != "failed" {
		t.Fatalf("run-scoped git gate must not inherit a checkout cwd, status=%s node=%+v", run.Status, run.Nodes["check"])
	}
}

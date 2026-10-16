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

func TestRepositoryScopedRoleGateAndScriptArtifacts(t *testing.T) {
	root := t.TempDir()
	repositories := make([]domain.RepositoryContext, 0, 2)
	for _, name := range []string{"frontend", "backend"} {
		source := filepath.Join(root, name+"-source")
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
				t.Fatalf("git %s %v: %v: %s", name, args, err, output)
			}
		}
		repositories = append(repositories, domain.RepositoryContext{
			RepositoryID: name, Name: name, CloneURL: source, BaseBranch: "master", WorkBranch: "feature/scoped-execution",
		})
	}
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	putGateTestAgent(t, s, "repository-validator", []domain.VerificationGate{{
		ID: "scope-gate", Name: "Scope gate", Mode: "command", Required: true,
		ExecutionScope: domain.ExecutionScopeRepositories,
		Commands:       []string{`test -s scope.txt && test "$AGENTFLOW_EXECUTION_SCOPE" = repositories`},
	}})
	wf := domain.WorkflowDefinition{
		ID: "repository-scope", Name: "Repository scope", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{
				ID: "prepare", Name: "Prepare", Type: "script", ScriptLanguage: "shell",
				ExecutionScope:  domain.ExecutionScopeRepositories,
				Script:          `printf '%s|%s|%s\n' "$AGENTFLOW_REPOSITORY_ID" "$AGENTFLOW_REPOSITORY_NAME" "$PWD" > scope.txt`,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "scope", Path: "scope.txt", MediaType: "text/plain", Required: true}},
				MaxAttempts:     1,
			},
			{
				ID: "validate", Name: "Validate", Type: "agent", AgentID: "repository-validator", ExecutorID: "mock",
				ExecutionScope: domain.ExecutionScopeRepositories, MaxAttempts: 1,
			},
			{
				ID: "consume", Name: "Consume", Type: "script", ScriptLanguage: "shell",
				ExecutionScope: domain.ExecutionScopeRun,
				Script:         `test -s .agentflow/inputs/prepare/frontend/scope.txt && test -s .agentflow/inputs/prepare/backend/scope.txt`,
				MaxAttempts:    1,
			},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "prepare-validate", Source: "prepare", Target: "validate", When: "success"},
			{ID: "validate-consume", Source: "validate", Target: "consume", When: "success"},
		},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	run, err := e.StartWithRepositoriesContext(wf.ID, "", repositories, domain.TelemetryContext{})
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
		t.Fatalf("run status = %s, node = %+v", run.Status, run.Nodes["prepare"])
	}
	seen := map[string]bool{}
	for _, artifact := range run.Artifacts {
		if artifact.Name == "scope" {
			seen[artifact.RepositoryDir] = strings.Contains(artifact.Content, artifact.RepositoryDir)
		}
	}
	for _, repository := range run.RepositoryContexts() {
		if !seen[repository.WorkspaceDir] {
			t.Fatalf("repository artifact missing for %s: %+v", repository.WorkspaceDir, run.Artifacts)
		}
	}
}

func TestRepositoryExecutionScopeRequiresRepository(t *testing.T) {
	if _, err := executionTargets(domain.Run{}, domain.ExecutionScopeRepositories); err == nil {
		t.Fatal("repository scope accepted a repository-less Run")
	}
}

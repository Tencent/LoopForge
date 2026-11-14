package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// A checkout whose git state cannot be inspected is logged and skipped; the
// run's other repositories are still checked and warned about.
func TestReportUndeliveredWorkContinuesAfterInspectionError(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	run := domain.Run{
		ID: "inspect-error-run", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"},
		Repositories: []domain.RepositoryContext{
			{RepositoryID: "repo-locked", Name: "locked", WorkspaceDir: "locked", BaseBranch: "main"},
			{RepositoryID: "repo-app", Name: "app", WorkspaceDir: "app", BaseBranch: "main"},
		},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := e.RunWorkspace(run.ID)

	// A corrupt index makes listing the untracked files fail. (A held
	// index.lock no longer does: the check works on a scratch copy of the
	// index and never writes the checkout's own.)
	locked := filepath.Join(workspace, "locked")
	undeliveredCheckout(t, locked)
	writeUndelivered(t, locked, "new.go", "package locked\n")
	writeUndelivered(t, filepath.Join(locked, ".git"), "index", "not a git index\n")

	app := filepath.Join(workspace, "app")
	undeliveredCheckout(t, app)
	writeUndelivered(t, app, "app.js", "console.log('v2')\n")

	e.reportUndeliveredWork(run.ID)

	warnings := undeliveredWarnings(t, st, run.ID)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "仓库 app") || !strings.Contains(warnings[0], "M app.js") {
		t.Fatalf("warnings = %q, want only the readable checkout", warnings)
	}
	if strings.Contains(warnings[0], "locked") || strings.Contains(warnings[0], "new.go") {
		t.Fatalf("uninspectable checkout reported: %s", warnings[0])
	}
}

// The authoritative checkout of a run on an execution machine is remote; a
// local directory with the same layout is stale and must not be inspected.
func TestReportUndeliveredWorkSkipsExecutionMachineWorkspace(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	run := domain.Run{
		ID: "remote-machine-run", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"},
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "runner-1"},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "repo-1", Name: "svc", WorkspaceDir: "svc", BaseBranch: "main"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(e.RunWorkspace(run.ID), "svc")
	undeliveredCheckout(t, checkout)
	writeUndelivered(t, checkout, "app.js", "console.log('stale local copy')\n")

	e.reportUndeliveredWork(run.ID)

	if warnings := undeliveredWarnings(t, st, run.ID); len(warnings) != 0 {
		t.Fatalf("execution-machine run reported local files: %q", warnings)
	}
}

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func undeliveredWarnings(t *testing.T, st interface {
	RunState(string) (domain.Run, bool)
}, runID string) []string {
	t.Helper()
	got, ok := st.RunState(runID)
	if !ok {
		t.Fatalf("run %s missing", runID)
	}
	var warnings []string
	for _, ev := range got.Events {
		if ev.Type == undeliveredChangesEvent {
			warnings = append(warnings, ev.Message)
		}
	}
	return warnings
}

// Only a succeeded run is checked: a cancelled run releases its workspace
// too, but its unfinished edits are expected and must not raise a delivery
// warning.
func TestCancelledRunDoesNotReportUndeliveredWork(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	run := domain.Run{
		ID: "cancelled-run", Status: "running",
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "repo-1", Name: "svc", WorkspaceDir: "svc", BaseBranch: "main"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(e.RunWorkspace(run.ID), "svc")
	undeliveredCheckout(t, checkout)
	writeUndelivered(t, checkout, "app.js", "console.log('half done')\n")

	e.finishAt(run.ID, run.Generation, "cancelled", "stopped")

	if got, _ := st.RunState(run.ID); got.Status != "cancelled" {
		t.Fatalf("status = %s", got.Status)
	}
	if warnings := undeliveredWarnings(t, st, run.ID); len(warnings) != 0 {
		t.Fatalf("cancelled run warned: %q", warnings)
	}
}

// reportUndeliveredWork re-reads the run and ignores anything that is not
// (or no longer) succeeded, runs without a local sandbox manager, and child
// SOPs working in their parent's checkout (the parent delivers that work).
func TestReportUndeliveredWorkSkipsRunsThatDoNotOwnDeliverableWork(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	repos := []domain.RepositoryContext{{RepositoryID: "repo-1", Name: "svc", WorkspaceDir: "svc", BaseBranch: "main"}}
	parent := "parent-run"
	checkout := filepath.Join(e.RunWorkspace(parent), "svc")
	undeliveredCheckout(t, checkout)
	writeUndelivered(t, checkout, "app.js", "console.log('dirty')\n")

	runs := []domain.Run{
		{ID: parent, Status: "failed", WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"}, Repositories: repos, Nodes: map[string]*domain.NodeRun{}},
		{ID: "child-run", Status: "succeeded", WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"}, Repositories: repos, Nodes: map[string]*domain.NodeRun{},
			ExecutionContext: domain.TelemetryContext{WorkspaceRunID: parent}},
	}
	for _, run := range runs {
		if err := st.PutRun(run); err != nil {
			t.Fatal(err)
		}
		e.reportUndeliveredWork(run.ID)
		if warnings := undeliveredWarnings(t, st, run.ID); len(warnings) != 0 {
			t.Fatalf("%s warned: %q", run.ID, warnings)
		}
	}

	// The same dirty checkout does warn once the owning run succeeded, which
	// proves the cases above were skipped and not just clean.
	succeeded := runs[0]
	succeeded.Status = "succeeded"
	if err := st.PutRun(succeeded); err != nil {
		t.Fatal(err)
	}
	sandboxes := e.sandboxes
	e.sandboxes = nil
	e.reportUndeliveredWork(parent)
	if warnings := undeliveredWarnings(t, st, parent); len(warnings) != 0 {
		t.Fatalf("run without sandbox manager warned: %q", warnings)
	}
	e.sandboxes = sandboxes
	e.reportUndeliveredWork(parent)
	if warnings := undeliveredWarnings(t, st, parent); len(warnings) != 1 {
		t.Fatalf("owning run warnings = %q, want 1", warnings)
	}
}

// Every writable checkout with undelivered work gets its own warning; a
// repository without a name is labelled by its workspace directory, and a
// directory that is not a git checkout is skipped.
func TestReportUndeliveredWorkWarnsPerRepository(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	run := domain.Run{
		ID: "multi-repo-run", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"},
		Repositories: []domain.RepositoryContext{
			{RepositoryID: "repo-1", Name: "frontend", WorkspaceDir: "frontend", BaseBranch: "main"},
			{RepositoryID: "repo-2", WorkspaceDir: "backend", BaseBranch: "main"},
			{RepositoryID: "repo-3", Name: "plain", WorkspaceDir: "plain", BaseBranch: "main"},
		},
		Nodes: map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := e.RunWorkspace(run.ID)
	for _, dir := range []string{"frontend", "backend"} {
		checkout := filepath.Join(workspace, dir)
		undeliveredCheckout(t, checkout)
		writeUndelivered(t, checkout, dir+".js", "export default 1\n")
	}
	if err := os.MkdirAll(filepath.Join(workspace, "plain"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeUndelivered(t, filepath.Join(workspace, "plain"), "notes.txt", "not a checkout\n")

	e.reportUndeliveredWork(run.ID)

	warnings := undeliveredWarnings(t, st, run.ID)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %q, want 2", warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"仓库 frontend", "A frontend.js", "仓库 backend", "A backend.js"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warnings missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "plain") {
		t.Fatalf("non-checkout directory reported: %s", joined)
	}
}

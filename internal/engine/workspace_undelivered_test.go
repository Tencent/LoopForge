package engine

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/runtime"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// #717: the run succeeded with the implement node's edits still uncommitted
// on the base branch. Settling it must say so before the workspace goes.
func TestSucceededRunReportsUndeliveredWork(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	run := domain.Run{
		ID: "undelivered-run", Status: "running",
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"},
		Repositories: []domain.RepositoryContext{
			{RepositoryID: "repo-1", Name: "frontend", WorkspaceDir: "frontend", BaseBranch: "main"},
			{RepositoryID: "repo-2", Name: "docs", WorkspaceDir: "docs", BaseBranch: "main", ReadOnly: true},
		},
		Nodes: map[string]*domain.NodeRun{"deliver": {NodeID: "deliver", Status: "succeeded", Attempt: 1}},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	workspace := e.RunWorkspace(run.ID)
	checkout := filepath.Join(workspace, "frontend")
	undeliveredCheckout(t, checkout)
	writeUndelivered(t, checkout, "app.js", "console.log('v2')\n")
	writeUndelivered(t, checkout, "page.js", "export const page = 1\n")
	readOnly := filepath.Join(workspace, "docs")
	undeliveredCheckout(t, readOnly)
	writeUndelivered(t, readOnly, "app.js", "read-only edits are not deliverable\n")

	e.finishAt(run.ID, run.Generation, "succeeded", "done")

	got, _ := st.RunState(run.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status = %s", got.Status)
	}
	var warnings []string
	for _, ev := range got.Events {
		if ev.Type == undeliveredChangesEvent {
			warnings = append(warnings, ev.Message)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q", warnings)
	}
	for _, want := range []string{"仓库 frontend", "2 个文件（+2/-1）", "M app.js", "A page.js", "打开工作区即可恢复"} {
		if !strings.Contains(warnings[0], want) {
			t.Fatalf("warning missing %q: %s", want, warnings[0])
		}
	}
}

func TestReportUndeliveredWorkIgnoresPushedWork(t *testing.T) {
	e, st := cvEEngine(t)
	e.sandboxes = runtime.NewManager(t.TempDir())
	run := domain.Run{
		ID: "delivered-run", Status: "succeeded",
		WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "local"},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "repo-1", Name: "svc", WorkspaceDir: "svc", BaseBranch: "main"}},
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(e.RunWorkspace(run.ID), "svc")
	undeliveredCheckout(t, checkout)
	writeUndelivered(t, checkout, "app.js", "console.log('pushed')\n")
	gitT(t, checkout, "commit", "-qam", "deliver")
	gitT(t, checkout, "push", "-q", "origin", "HEAD:refs/heads/agentflow/work")

	e.reportUndeliveredWork(run.ID)
	got, _ := st.RunState(run.ID)
	for _, ev := range got.Events {
		if ev.Type == undeliveredChangesEvent {
			t.Fatalf("pushed work reported as undelivered: %s", ev.Message)
		}
	}
}

func TestUndeliveredWorkMessageCapsFileList(t *testing.T) {
	files := make([]gitwork.PatchFileSummary, undeliveredFilesListed+3)
	for i := range files {
		files[i] = gitwork.PatchFileSummary{Path: fmt.Sprintf("f%02d.go", i), Status: "M", Added: 2, Deleted: 1}
	}
	msg := undeliveredWorkMessage(domain.RepositoryContext{}, files)
	for _, want := range []string{"仓库 工作区", "23 个文件（+46/-23）", "M f19.go", "… 另有 3 个文件"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "f20.go") {
		t.Fatalf("message lists too many files: %s", msg)
	}
}

func writeUndelivered(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// undeliveredCheckout clones a one-commit repository into dir, as a run
// checkout of the base branch.
func undeliveredCheckout(t *testing.T, dir string) {
	t.Helper()
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	gitT(t, source, "init", "-q", "-b", "main")
	gitT(t, source, "config", "user.name", "Test")
	gitT(t, source, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(source, "app.js"), []byte("console.log('v1')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, source, "add", "app.js")
	gitT(t, source, "commit", "-qm", "baseline")
	gitT(t, root, "clone", "-q", "--bare", source, remote)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "clone", "-q", remote, dir)
	gitT(t, dir, "config", "user.name", "Test")
	gitT(t, dir, "config", "user.email", "test@example.com")
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

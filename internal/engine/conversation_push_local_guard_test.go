package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func remoteBranches(t *testing.T, remote, pattern string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", remote, "branch", "--list", pattern).CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list %s: %v %s", pattern, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A repository bound with RequireExistingWorkBranch must only advance an
// existing remote branch; it may never fall back to creating one.
func TestPushLocalConversationRepositoryRequireExistingNeverCreatesBranch(t *testing.T) {
	workspace, checkout, remote := newLocalPushCheckout(t)
	testGit(t, checkout, "checkout", "-b", "feature/continue")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc := domain.RepositoryContext{RepositoryID: "repo-1", Name: "app", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/continue", WorkspaceDir: "app", RequireExistingWorkBranch: true}

	entry, pushed, err := pushLocalConversationRepository(localPushContext(), workspace, rc, "msg", "")
	if err == nil || pushed || entry != (domain.RunnerWorkBranchHead{}) {
		t.Fatalf("missing existing branch must fail: entry=%+v pushed=%v err=%v", entry, pushed, err)
	}
	if !strings.HasPrefix(err.Error(), "app: git push feature/continue: ") {
		t.Fatalf("push failure must name the repository and branch: %v", err)
	}
	if got := remoteBranches(t, remote, "feature/*"); got != "" {
		t.Fatalf("an existing-branch push must not create the remote branch: %s", got)
	}
}

func TestPushLocalConversationRepositoryRequireExistingAdvancesBranch(t *testing.T) {
	workspace, checkout, remote := newLocalPushCheckout(t)
	testGit(t, remote, "branch", "feature/continue", "master")
	testGit(t, checkout, "fetch", "origin")
	testGit(t, checkout, "checkout", "-b", "feature/continue", "origin/feature/continue")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("continued\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc := domain.RepositoryContext{RepositoryID: "repo-1", Name: "app", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/continue", WorkspaceDir: "app", RequireExistingWorkBranch: true}

	entry, pushed, err := pushLocalConversationRepository(localPushContext(), workspace, rc, "fix: 继续", "")
	if err != nil || !pushed {
		t.Fatalf("pushed=%v err=%v", pushed, err)
	}
	if got := testGit(t, remote, "rev-parse", "refs/heads/feature/continue"); got != entry.Head || entry.Head == "" {
		t.Fatalf("remote %s != pushed %s", got, entry.Head)
	}
}

// A merge conflict against the shared branch is an error: the caller must
// not record the repository as pushed and the shared branch stays untouched.
func TestPushLocalConversationRepositorySharedConflictIsNotReportedAsPushed(t *testing.T) {
	workspace, checkout, remote := newLocalPushCheckout(t)
	// preview diverges from master on the same line the work branch edits.
	other := filepath.Join(filepath.Dir(workspace), "other")
	if out, err := exec.Command("git", "clone", remote, other).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	testGit(t, other, "config", "user.email", "bob@example.com")
	testGit(t, other, "config", "user.name", "bob")
	testGit(t, other, "checkout", "-b", "preview")
	if err := os.WriteFile(filepath.Join(other, "app.txt"), []byte("preview\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, other, "commit", "-am", "preview")
	testGit(t, other, "push", "origin", "preview")
	previewBefore := testGit(t, remote, "rev-parse", "refs/heads/preview")

	testGit(t, checkout, "checkout", "-b", "feature/local")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc := domain.RepositoryContext{RepositoryID: "repo-1", Name: "app", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/local", WorkspaceDir: "app"}

	entry, pushed, err := pushLocalConversationRepository(localPushContext(), workspace, rc, "msg", "preview")
	if err == nil || pushed || entry != (domain.RunnerWorkBranchHead{}) {
		t.Fatalf("conflict must fail: entry=%+v pushed=%v err=%v", entry, pushed, err)
	}
	if !strings.HasPrefix(err.Error(), "app: ") || !strings.Contains(err.Error(), "有冲突") || !strings.Contains(err.Error(), "app.txt") {
		t.Fatalf("conflict error must name repository and file: %v", err)
	}
	if got := testGit(t, remote, "rev-parse", "refs/heads/preview"); got != previewBefore {
		t.Fatalf("shared branch moved on conflict: %s -> %s", previewBefore, got)
	}
	if branch := testGit(t, checkout, "branch", "--show-current"); branch != "feature/local" {
		t.Fatalf("checkout left the work branch: %s", branch)
	}
}

// A shared branch that does not exist remotely is never created and the
// failure is reported with the repository and target names.
func TestPushLocalConversationRepositoryMissingSharedBranchFails(t *testing.T) {
	workspace, checkout, remote := newLocalPushCheckout(t)
	testGit(t, checkout, "checkout", "-b", "feature/local")
	if err := os.WriteFile(filepath.Join(checkout, "app.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rc := domain.RepositoryContext{RepositoryID: "repo-1", Name: "app", CloneURL: remote, BaseBranch: "master", WorkBranch: "feature/local", WorkspaceDir: "app"}

	entry, pushed, err := pushLocalConversationRepository(localPushContext(), workspace, rc, "msg", "preview")
	if err == nil || pushed || entry != (domain.RunnerWorkBranchHead{}) {
		t.Fatalf("missing shared branch must fail: entry=%+v pushed=%v err=%v", entry, pushed, err)
	}
	if !strings.HasPrefix(err.Error(), "app: 直推 preview: ") {
		t.Fatalf("shared push failure must name repository and target: %v", err)
	}
	if got := remoteBranches(t, remote, "preview"); got != "" {
		t.Fatalf("missing shared branch must not be created: %s", got)
	}
}

func newMultiRepoPushEngine(t *testing.T, repositories []domain.RepositoryContext) (*Engine, string) {
	t.Helper()
	root := t.TempDir()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := New(st, runtime.NewManager(filepath.Join(root, "workspaces")))
	run := domain.Run{
		ID:               "run-multi-push",
		Status:           "waiting",
		Input:            "改文案",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "free-collaboration", Name: "自由协作", CollaborationMode: "group"},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ExecutionMode: domain.ExecutionModeCloud},
		Repositories:     repositories,
		Nodes:            map[string]*domain.NodeRun{},
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return e, e.sandboxes.RunWorkspace(run.ID)
}

func cloneInto(t *testing.T, remote, checkout, branch string) {
	t.Helper()
	if out, err := exec.Command("git", "clone", remote, checkout).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	testGit(t, checkout, "checkout", "-b", branch)
}

// Unchanged repositories are left out of the returned heads, and a later
// failure still returns the heads already pushed.
func TestPushConversationWorkspaceSkipsUnchangedAndKeepsPushedHeadsOnFailure(t *testing.T) {
	_, _, remoteA := newLocalPushCheckout(t)
	_, _, remoteB := newLocalPushCheckout(t)
	_, _, remoteC := newLocalPushCheckout(t)
	repositories := []domain.RepositoryContext{
		{RepositoryID: "repo-a", Name: "a", CloneURL: remoteA, BaseBranch: "master", WorkBranch: "feature/a", WorkspaceDir: "a"},
		{RepositoryID: "repo-b", Name: "b", CloneURL: remoteB, BaseBranch: "master", WorkBranch: "feature/b", WorkspaceDir: "b"},
	}
	e, root := newMultiRepoPushEngine(t, repositories)
	cloneInto(t, remoteA, filepath.Join(root, "a"), "feature/a")
	cloneInto(t, remoteB, filepath.Join(root, "b"), "feature/b")
	if err := os.WriteFile(filepath.Join(root, "b", "app.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	heads, err := e.PushConversationWorkspace(context.Background(), "run-multi-push", 0, "token", "alice", "alice@example.com", "fix: b", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 1 || heads[0].RepositoryID != "repo-b" || heads[0].Head == "" {
		t.Fatalf("only the changed repository may be reported: %+v", heads)
	}
	if got := remoteBranches(t, remoteA, "feature/*"); got != "" {
		t.Fatalf("unchanged repository must not be pushed: %s", got)
	}

	// Add a third repository whose checkout drifted to another branch: the
	// error is returned together with the heads pushed before it.
	run, _ := e.repo.RunState("run-multi-push")
	run.Repositories = append(run.Repositories, domain.RepositoryContext{RepositoryID: "repo-c", Name: "c", CloneURL: remoteC, BaseBranch: "master", WorkBranch: "feature/c", WorkspaceDir: "c"})
	if err := e.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	cloneInto(t, remoteC, filepath.Join(root, "c"), "feature/manual")
	if err := os.WriteFile(filepath.Join(root, "c", "app.txt"), []byte("drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b", "app.txt"), []byte("changed again\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	partial, err := e.PushConversationWorkspace(context.Background(), "run-multi-push", 0, "token", "alice", "alice@example.com", "fix: b again", "")
	if err == nil || !strings.HasPrefix(err.Error(), "c: ") {
		t.Fatalf("drifted repository must fail with its name: %v", err)
	}
	if len(partial) != 1 || partial[0].RepositoryID != "repo-b" || partial[0].Head == heads[0].Head {
		t.Fatalf("heads pushed before the failure must be returned: %+v", partial)
	}
	if got := testGit(t, remoteB, "rev-parse", "refs/heads/feature/b"); got != partial[0].Head {
		t.Fatalf("remote b %s != reported %s", got, partial[0].Head)
	}
	if got := remoteBranches(t, remoteC, "feature/*"); got != "" {
		t.Fatalf("drifted repository must not be pushed: %s", got)
	}
}

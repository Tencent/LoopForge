package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSOPReusesConversationCheckoutAfterTurn(t *testing.T) {
	t.Parallel()
	testSOPReusesConversationCheckout(t, false)
}

func TestSOPReusesConversationCheckoutInDocker(t *testing.T) {
	if os.Getenv("AGENTFLOW_TEST_DOCKER") != "1" {
		t.Skip("set AGENTFLOW_TEST_DOCKER=1 for local Docker sandbox tests")
	}
	dockerRoot := os.Getenv("AGENTFLOW_DOCKER_TEST_ROOT")
	if dockerRoot == "" {
		dockerRoot = "."
	}
	dockerRoot, err := filepath.Abs(dockerRoot)
	if err != nil {
		t.Fatal(err)
	}
	temp, err := os.MkdirTemp(dockerRoot, ".sop-checkout-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temp) })
	t.Setenv("TMPDIR", temp)
	t.Setenv("AGENTFLOW_DOCKER_NETWORK", "none")
	testSOPReusesConversationCheckout(t, true)
}

func testSOPReusesConversationCheckout(t *testing.T, docker bool) {
	t.Helper()
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const projectID = "conversation-workspace-project"
	if err := st.PutProject(domain.Project{ID: projectID, Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	sopSandbox := repo.TestNativeSandboxID
	if docker {
		sopSandbox = "conversation-sop-docker"
		if err := st.PutSandbox(domain.SandboxProfile{ID: sopSandbox, Name: "LOOP Docker", ProjectID: projectID,
			Driver: domain.SandboxDriverDocker, Dockerfile: &domain.SandboxDockerfile{
				Content: "FROM golang:1.26.11-bookworm\nCOPY --chmod=0755 git /usr/local/bin/git\n",
				Files: map[string]string{"git": `#!/bin/sh
mkdir -p /workspace/.agentflow
printf '%s\t%s\n' "$PWD" "$*" >> /workspace/.agentflow/sop-git.log
exec /usr/bin/git "$@"
`},
			}}); err != nil {
			t.Fatal(err)
		}
	}
	manager := runtime.NewManager(t.TempDir())
	en := New(st, manager)
	if docker {
		defer func() {
			for _, run := range st.Runs() {
				if run.ExecutionContext.ProjectID == projectID && !run.IsWorkspace() {
					manager.DiscardRunContainer(context.Background(), run.ID)
				}
			}
		}()
	}
	owner, err := en.StartConversationWorkspace("Prepare a fix", domain.TelemetryContext{
		ProjectID: projectID, ExecutionPinned: true,
	}, repo.TestNativeSandboxID)
	if err != nil {
		t.Fatal(err)
	}
	root := en.RunWorkspace(owner.ID)
	remote := t.TempDir()
	gitInit(t, remote)
	gitCommitFile(t, remote, "tracked.txt", "baseline")
	const checkout = "code/checkout"
	dir := filepath.Join(root, checkout)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(at string, args ...string) string {
		t.Helper()
		out, err := gitC(context.Background(), at, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	gitInit(t, root)
	git(root, "remote", "add", "origin", "https://example.invalid/outer.git")
	git(root, "clone", remote, dir)
	git(dir, "config", "user.email", "test@example.com")
	git(dir, "config", "user.name", "Test")
	gitCommitFile(t, dir, "local-commit.txt", "unpushed")
	head := git(dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("unstaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(dir, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(root, "handoff.txt"), []byte("conversation"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := domain.WorkflowDefinition{ID: "child-sop", Name: "Child LOOP", SandboxProfileID: sopSandbox,
		Nodes: []domain.WorkflowNode{{ID: "verify", Name: "Verify", Type: "script", ScriptLanguage: "shell", Script: `
test "$(cat handoff.txt)" = conversation &&
test "$(cat code/checkout/local-commit.txt)" = unpushed &&
test "$(cat code/checkout/tracked.txt)" = unstaged &&
test "$(git -C code/checkout diff --cached --name-only)" = staged.txt &&
printf sop > result.txt`, MaxAttempts: 1}},
	}
	wf := domain.WorkflowDefinition{ID: "workspace-sop", Name: "LOOP", SandboxProfileID: sopSandbox,
		Nodes: []domain.WorkflowNode{{ID: "child", Name: "Child LOOP", Type: "sop", SOPWorkflowID: child.ID, MaxAttempts: 1}},
	}
	for _, workflow := range []domain.WorkflowDefinition{child, wf} {
		if err := st.PutWorkflow(workflow); err != nil {
			t.Fatal(err)
		}
	}
	token, err := en.BeginConversationWorkspaceUse(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	ec := domain.TelemetryContext{ProjectID: projectID, WorkspaceRunID: owner.ID}
	repositories := []domain.RepositoryContext{{RepositoryID: "repo", Name: "Configured repo name", LocalPath: remote, BaseBranch: "master"}}
	started, err := en.StartWithRepositoriesContext(wf.ID, "Continue work", repositories, ec)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, st, started.ID, 5*time.Second, func(run domain.Run) bool {
		return run.Status == "running" && run.Nodes["child"].Status == "running"
	})
	if _, err := en.StartWithRepositoriesContext(wf.ID, "Another LOOP", repositories, ec); !errors.Is(err, repo.ErrWorkspaceInUse) {
		t.Fatalf("concurrent LOOP = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("LOOP wrote before the conversation released the workspace: %v", err)
	}
	if err := st.FinishWorkspaceUse(owner.ID, token, time.Now().UTC(), nil, ""); err != nil {
		t.Fatal(err)
	}
	timeout := 10 * time.Second
	if docker {
		timeout = time.Minute
	}
	done := waitRun(t, st, started.ID, timeout, func(run domain.Run) bool {
		return domain.TerminalRunStatus(run.Status)
	})
	childRun, _ := st.RunState(done.Nodes["child"].ChildRunID)
	if done.Status != "succeeded" {
		log, _ := os.ReadFile(filepath.Join(root, ".agentflow", "sop-git.log"))
		t.Fatalf("LOOP %s in %q, child in %q: %s, verification: %+v\nsandbox Git:\n%s",
			done.Status, done.Repository.WorkspaceDir, childRun.Repository.WorkspaceDir,
			done.Nodes["child"].Error, childRun.Nodes["verify"], log)
	}
	if en.RunWorkspace(done.ID) != root || en.RunWorkspace(childRun.ID) != root ||
		done.Repository.WorkspaceDir != checkout || childRun.Repository.WorkspaceDir != checkout {
		t.Fatalf("LOOP and child must use %s and %s, got %+v and %+v", root, checkout, done.Repository, childRun.Repository)
	}
	if got := git(dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("LOOP changed the checkout's starting commit: %s, want %s", got, head)
	}
	if got := git(dir, "branch", "--show-current"); got != done.Repository.WorkBranch {
		t.Fatalf("checked-out branch = %q, want %q", got, done.Repository.WorkBranch)
	}
	if data, err := os.ReadFile(filepath.Join(root, "result.txt")); err != nil || string(data) != "sop" {
		t.Fatalf("LOOP output in original workspace = %q, %v", data, err)
	}
	if docker {
		log, err := os.ReadFile(filepath.Join(root, ".agentflow", "sop-git.log"))
		if err != nil || !strings.Contains(string(log), "config --local --no-includes --get-regexp") ||
			!strings.Contains(string(log), "checkout --quiet -b "+done.Repository.WorkBranch) {
			t.Fatalf("LOOP sandbox did not discover and switch the shared checkout: %s (%v)", log, err)
		}
	}
	if err := en.ReleaseConversationWorkspace(owner.ID); err != nil {
		t.Fatal(err)
	}
	en.cleanupRunWorkspace(owner.ID)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("released checkout was not reclaimed: %v", err)
	}
	next, err := en.StartWithRepositoriesContext(wf.ID, "Continue from the reclaimed workspace", repositories, ec)
	if err != nil {
		t.Fatal(err)
	}
	restored := waitRun(t, st, next.ID, timeout, func(run domain.Run) bool {
		return domain.TerminalRunStatus(run.Status)
	})
	if restored.Status != "succeeded" || en.RunWorkspace(restored.ID) != root {
		childRun, _ := st.RunState(restored.Nodes["child"].ChildRunID)
		t.Fatalf("LOOP did not continue from the restored checkout: %s, verification: %+v", restored.Nodes["child"].Error, childRun.Nodes["verify"])
	}
}

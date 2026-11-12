package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestRunnerIntegrateAndConversationPushCanceled(t *testing.T) {
	e, st := cvEEngine(t)
	runner := cvERunner(t, e, "integration-runner", domain.RunnerStatusOnline, "terminal")
	run := cvERunnerRun("integration-run", runner.ID)
	run.Input = "delivery"
	run.Repositories = []domain.RepositoryContext{{
		RepositoryID: "repo", Name: "Repo", CloneURL: "https://example/repo.git",
		WorkspaceDir: "repo", WorkBranch: "work", IntegrateBranch: "integrate",
	}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.integrateOnRunner(ctx, runner, run, domain.RepositoryContext{RepositoryID: "missing"}, false); err == nil {
		t.Fatal("missing integration repository accepted")
	}
	if _, err := e.integrateOnRunner(ctx, runner, run, run.Repositories[0], false); !errors.Is(err, context.Canceled) {
		t.Fatalf("integrate cancel = %v", err)
	}
	if _, err := e.pushConversationOnRunner(ctx, runner.ID, run, run.Repositories, 0, "token", "name", "mail", "message", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("push cancel = %v", err)
	}
	legacy := run
	legacy.ExecutionContext.RunnerWorkspaceLayout = ""
	if _, err := e.pushConversationOnRunner(context.Background(), runner.ID, legacy, legacy.Repositories, 0, "token", "", "", "message", ""); err == nil {
		t.Fatal("legacy runner layout pushed")
	}
	if _, err := e.pushConversationOnRunner(context.Background(), "missing", run, run.Repositories, 0, "token", "", "", "message", ""); err == nil {
		t.Fatal("missing runner pushed")
	}
	if _, err := e.PushConversationWorkspace(context.Background(), "missing", 0, "token", "", "", "", ""); err == nil {
		t.Fatal("missing run pushed")
	}
	if _, err := e.PushConversationWorkspace(context.Background(), run.ID, 0, "", "", "", "", ""); err == nil {
		t.Fatal("missing token pushed")
	}
	noRepos := run
	noRepos.ID = "push-no-repos"
	noRepos.Repositories = nil
	noRepos.Repository = domain.RepositoryContext{}
	if err := st.PutRun(noRepos); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PushConversationWorkspace(context.Background(), noRepos.ID, 0, "token", "", "", "", ""); err == nil {
		t.Fatal("repository-less run pushed")
	}
	if got := truncateCommitMessage(strings.Repeat("界", 2100)); len([]rune(got)) != 2000 {
		t.Fatalf("truncated runes = %d", len([]rune(got)))
	}
	if truncateCommitMessage("short") != "short" {
		t.Fatal("short commit changed")
	}
}

func TestIntegrationCallbacksRepairPatchAndCompleteness(t *testing.T) {
	e, st := cvEEngine(t)
	run := domain.Run{ID: "integration-callback", Status: "running", Input: "Title", WorkflowSnapshot: domain.WorkflowDefinition{
		Nodes: []domain.WorkflowNode{{ID: "implement", Type: "agent"}, {ID: "review", Type: "approval", RejectTo: "implement"}},
	}, Nodes: map[string]*domain.NodeRun{"implement": {NodeID: "implement", Status: "succeeded"}, "review": {NodeID: "review", Status: "failed"}}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	rc := domain.RepositoryContext{RepositoryID: "repo", Name: "Repo", GongfengProjectID: "group/repo", WorkBranch: "work", IntegrateBranch: "integrate"}
	e.MergeIntoBranch = func(project, source, target, title, description string) (string, error) {
		if project == "" || source != "work" || target != "integrate" || title == "" || description == "" {
			t.Fatalf("merge args: %q %q %q %q %q", project, source, target, title, description)
		}
		return "!1", nil
	}
	if err := e.mergeWorkBranchIntoIntegrate(run.ID, run, rc, "work", "integrate", func() error { return errors.New("must not call") }); err != nil {
		t.Fatal(err)
	}
	e.MergeIntoBranch = func(string, string, string, string, string) (string, error) { return "", errors.New("merge failed") }
	if err := e.mergeWorkBranchIntoIntegrate(run.ID, run, rc, "work", "integrate", func() error { return nil }); err == nil {
		t.Fatal("merge failure ignored")
	}
	e.MergeIntoBranch = nil
	if err := e.mergeWorkBranchIntoIntegrate(run.ID, run, rc, "work", "integrate", func() error { return errors.New("push failed") }); err == nil {
		t.Fatal("push failure ignored")
	}
	if err := e.mergeWorkBranchIntoIntegrate(run.ID, run, rc, "work", "integrate", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := integrationRepairNode(run.WorkflowSnapshot); got != "implement" {
		t.Fatal(got)
	}
	if got := integrationRepairNode(domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "code-fix", Type: "agent"}}}); got != "code-fix" {
		t.Fatal(got)
	}
	if got := integrationRepairNode(domain.WorkflowDefinition{}); got != "" {
		t.Fatal(got)
	}
	if files := patchNewFiles("A\tone.txt\nM\ttwo.txt\nA\tthree.txt"); len(files) != 2 {
		t.Fatalf("inventory files = %v", files)
	}
	diff := "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n"
	if files := patchNewFiles(diff); len(files) != 1 || files[0] != "new.txt" {
		t.Fatalf("diff files = %v", files)
	}
	if files := patchNewFiles("plain"); len(files) != 0 {
		t.Fatalf("plain files = %v", files)
	}
	if _, onRunner, err := e.integrationRunner(domain.Run{ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeCloud}}); err != nil || onRunner {
		t.Fatalf("cloud integration runner = %v %v", onRunner, err)
	}
	missing := cvERunnerRun("r", "missing")
	if _, _, err := e.integrationRunner(missing); err == nil {
		t.Fatal("missing integration runner accepted")
	}

	repoDir := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "cve@example.com"}, {"config", "user.name", "CvE"}} {
		if out, err := runGit(context.Background(), repoDir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "present.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(context.Background(), repoDir, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(context.Background(), repoDir, "commit", "-m", "base"); err != nil {
		t.Fatal(err)
	}
	verify := domain.Run{Artifacts: []domain.Artifact{
		{Name: "repository-patch", NodeID: "build", Content: "A\tmissing.txt\nA\tpresent.txt"},
	}}
	missingFiles := e.verifyDeliveryCompleteness(verify, repoDir)
	if len(missingFiles) != 1 || !strings.Contains(missingFiles[0], "missing.txt") {
		t.Fatalf("missing delivery = %v", missingFiles)
	}
	if got := e.verifyDeliveryCompleteness(domain.Run{}, repoDir); got != nil {
		t.Fatalf("empty completeness = %v", got)
	}
}

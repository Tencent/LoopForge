package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func cvRInstallRunnerDocker(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  ps)
    [ "$CVR_DOCKER_MODE" = ps-error ] && { echo "ps failed" >&2; exit 1; }
    printf 'owned gone foreign\n'
    ;;
  inspect)
    if [ "$4" = gone ]; then echo "No such container" >&2; exit 1; fi
    if [ "$CVR_DOCKER_MODE" = inspect-error ] && [ "$4" = owned ]; then echo "inspect failed" >&2; exit 1; fi
    case "$3" in
      *Mounts*) [ "$4" = owned ] && printf '%s/node\n' "$CVR_WORKSPACE" || printf '/tmp/foreign\n' ;;
      *) printf 'agentflow-tmp-rt-cvr\n' ;;
    esac
    ;;
  rm)
    [ "$CVR_DOCKER_MODE" = rm-gone ] && { echo "container not found" >&2; exit 1; }
    [ "$CVR_DOCKER_MODE" = rm-error ] && { echo "remove failed" >&2; exit 1; }
    :
    ;;
  rmi) : ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestSearchWorkspaceResultsLimitsAndCancellation(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("界", 260)
	body := "first Needle result\n" + long + " needle\n"
	if err := os.WriteFile(filepath.Join(root, "match.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large"), make([]byte, 256*1024+1), 0o600); err != nil {
		t.Fatal(err)
	}
	results, truncated, err := runnerSearchWorkspace(context.Background(), root, "NEEDLE")
	if err != nil || truncated || len(results) != 2 || !strings.HasSuffix(results[1].Preview, "…") {
		t.Fatalf("results=%+v truncated=%v err=%v", results, truncated, err)
	}
	many := strings.Repeat("needle\n", 101)
	if err := os.WriteFile(filepath.Join(root, "many.txt"), []byte(many), 0o600); err != nil {
		t.Fatal(err)
	}
	results, truncated, err = runnerSearchWorkspace(context.Background(), root, "needle")
	if err != nil || !truncated || len(results) != 100 {
		t.Fatalf("limited results=%d truncated=%v err=%v", len(results), truncated, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := runnerSearchWorkspace(ctx, root, "needle"); err == nil {
		t.Fatal("canceled search succeeded")
	}
}

func TestWorkspaceLogicalPathsAndRepositoryDefaults(t *testing.T) {
	for _, raw := range []string{"/absolute", "../escape", ".git/config", "bad\x00path"} {
		if _, err := runnerWorkspaceLogicalPath(raw); err == nil {
			t.Fatalf("unsafe path accepted: %q", raw)
		}
	}
	if got, err := runnerWorkspaceLogicalPath(" ./dir/file "); err != nil || got != "dir/file" {
		t.Fatalf("logical path=%q err=%v", got, err)
	}
	root := t.TempDir()
	if roots := runnerWorkspaceRepositoryRoots(root, nil); len(roots) != 1 || roots[0].path != root || roots[0].label != "" {
		t.Fatalf("default roots=%+v", roots)
	}
	if path, label, ok := runnerFindWorkspaceRepository(root, nil, "workspace"); !ok || path != root || label != "" {
		t.Fatalf("default repository path=%q label=%q ok=%v", path, label, ok)
	}
}

func TestReplaceCheckoutContentsSuccess(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	replacement := filepath.Join(parent, "replacement")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(replacement, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "old"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "new"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceCheckoutContents(workspace, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "new")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "old")); !os.IsNotExist(err) {
		t.Fatalf("old checkout remains: %v", err)
	}
}

func TestReadWorkspaceSearchAndUnsupported(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "runs", "search", "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("find me here"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: base}}
	request := domain.RunnerWorkspaceRequest{
		RunID: "search", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		Operation: domain.RunnerWorkspaceOperationSearch, Query: "find",
	}
	if response := d.readRunnerWorkspace(context.Background(), request); response.Status != 200 {
		t.Fatalf("search response=%+v", response)
	}
	request.Operation = "cvR-unsupported"
	if response := d.readRunnerWorkspace(context.Background(), request); response.Status != 400 {
		t.Fatalf("unsupported response=%+v", response)
	}
}

func TestCleanupDockerWorkspaceBranches(t *testing.T) {
	cvRInstallRunnerDocker(t)
	workspace := t.TempDir()
	t.Setenv("CVR_WORKSPACE", workspace)
	for _, mode := range []string{"", "rm-gone"} {
		t.Setenv("CVR_DOCKER_MODE", mode)
		if err := cleanupDockerWorkspace(context.Background(), workspace); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
	for _, mode := range []string{"ps-error", "inspect-error", "rm-error"} {
		t.Setenv("CVR_DOCKER_MODE", mode)
		if err := cleanupDockerWorkspace(context.Background(), workspace); err == nil {
			t.Fatalf("mode %q unexpectedly succeeded", mode)
		}
	}
}

func TestWorkspaceLargeSearchAndGitEdges(t *testing.T) {
	root := t.TempDir()
	chunk := make([]byte, 256*1024)
	for i := 0; i < 81; i++ {
		name := filepath.Join(root, "bulk-"+string(rune('a'+i/26))+string(rune('a'+i%26)))
		if err := os.WriteFile(name, chunk, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, truncated, err := runnerSearchWorkspace(context.Background(), root, "absent"); err != nil || !truncated {
		t.Fatalf("large search truncated=%v err=%v", truncated, err)
	}

	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cvRGit(t, repo, "init", "-q")
	cvRGit(t, repo, "config", "user.name", "cvR")
	cvRGit(t, repo, "config", "user.email", "cvr@example.test")
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "large.txt"), make([]byte, runnerWorkspaceFileLimit+8), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "binary.txt"), []byte{0, 1}, 0o600); err != nil {
		t.Fatal(err)
	}
	cvRGit(t, repo, "add", ".")
	cvRGit(t, repo, "commit", "-qm", "first")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	cvRGit(t, repo, "add", ".")
	cvRGit(t, repo, "commit", "-qm", "second")
	repositories := []domain.RepositoryContext{{WorkspaceDir: "repo"}, {WorkspaceDir: "repo"}}
	if names := runnerCollectWorkspaceGitRepositories(root, repositories); len(names) != 1 {
		t.Fatalf("repository names=%v", names)
	}
	log, err := runnerCollectWorkspaceLog(context.Background(), root, repositories[:1], "repo")
	if err != nil || len(log["commits"].([]runnerWorkspaceCommit)) < 2 {
		t.Fatalf("log=%v err=%v", log, err)
	}
	head := strings.TrimSpace(cvRGit(t, repo, "rev-parse", "HEAD"))
	files, err := runnerCollectWorkspaceCommitFiles(context.Background(), root, repositories[:1], "repo", head)
	if err != nil || len(files) != 1 || files[0].Path != "repo/base.txt" {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	if _, truncated, err := runnerReadWorkspaceBaseFile(context.Background(), root, repositories[:1], "repo/large.txt"); err != nil || !truncated {
		t.Fatalf("large base truncated=%v err=%v", truncated, err)
	}
	if _, _, err := runnerReadWorkspaceBaseFile(context.Background(), root, repositories[:1], "repo/binary.txt"); err == nil {
		t.Fatal("binary base accepted")
	}
	if _, _, err := runnerReadWorkspaceDiff(context.Background(), root, repositories[:1], "../bad"); err == nil {
		t.Fatal("unsafe diff path accepted")
	}
}

func TestAlignIntegrateAndOutputErrors(t *testing.T) {
	if _, err := alignBaseline(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("nil align invocation accepted")
	}
	if _, err := alignBaseline(context.Background(), t.TempDir(), &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{}}}); err == nil {
		t.Fatal("align without branches accepted")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{IntegrateBranch: "integration", DefaultBranch: "main"}}}
	if _, err := alignBaseline(context.Background(), blocked, inv); err == nil {
		t.Fatal("align file directory accepted")
	}
	if _, err := alignBaseline(context.Background(), t.TempDir(), inv); err == nil {
		t.Fatal("align with missing remote accepted")
	}
	if results, err := integrateTaskRepositories(context.Background(), t.TempDir(), nil, "label"); err != nil || len(results) != 0 {
		t.Fatalf("nil integrate results=%v err=%v", results, err)
	}
	skip := &domain.RunnerInvocation{Repositories: []domain.RunnerRepository{{WorkBranch: "work"}}}
	if results, err := integrateTaskRepositories(context.Background(), t.TempDir(), skip, "label"); err != nil || len(results) != 0 {
		t.Fatalf("skipped integrate results=%v err=%v", results, err)
	}
	bad := &domain.RunnerInvocation{ArchivedFiles: []domain.WorkspaceArtifactFile{{Path: "../bad", Content: "x", SHA256: "bad"}}}
	if err := prepareInvocationWorkspace(t.TempDir(), bad); err == nil {
		t.Fatal("invalid archived output accepted")
	}
	workspace := t.TempDir()
	target := filepath.Join(workspace, ".agentflow", "inputs", "value")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeInputFile(workspace, domain.RunnerInputFile{Path: ".agentflow/inputs/value", Content: "x"}); err == nil {
		t.Fatal("directory input target accepted")
	}
	if err := writeInputFile(workspace, domain.RunnerInputFile{Path: "../unsafe", Content: "x"}); err == nil {
		t.Fatal("unsafe input path accepted")
	}
	if err := prepareInvocationWorkspace(workspace, &domain.RunnerInvocation{GateResultPath: "../unsafe"}); err == nil {
		t.Fatal("unsafe gate result accepted")
	}
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}}
	taskWorkspace := filepath.Join(d.cfg.DefaultWorkspaceRoot, "occupied")
	if err := os.WriteFile(taskWorkspace, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := d.executeTerminal(context.Background(), domain.RunnerTask{ID: "occupied", Workspace: "occupied"}); result.status != domain.RunnerTaskFailed {
		t.Fatalf("terminal file workspace=%+v", result)
	}
}

func TestWorkspaceRemainingRepositoryEdges(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plain.txt"), []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	repositories := []domain.RepositoryContext{{WorkspaceDir: "other"}}
	if roots := runnerWorkspaceRepositoryRoots(root, []domain.RepositoryContext{{WorkspaceDir: "."}}); len(roots) != 1 || roots[0].label != "" {
		t.Fatalf("dot roots=%+v", roots)
	}
	if path, _, ok := runnerFindWorkspaceRepository(root, repositories, ""); !ok || path != filepath.Join(root, "other") {
		t.Fatalf("single repository path=%q ok=%v", path, ok)
	}
	if body, truncated, err := runnerReadWorkspaceBaseFile(context.Background(), root, repositories, "plain.txt"); err != nil || body != "" || truncated {
		t.Fatalf("base body=%q truncated=%v err=%v", body, truncated, err)
	}
	if body, size, err := runnerReadWorkspaceGitBlob(context.Background(), root, repositories, "plain.txt", "abcdef0"); err != nil || body != "" || size != 0 {
		t.Fatalf("blob body=%q size=%d err=%v", body, size, err)
	}
	if _, err := runnerCollectWorkspaceCommitFiles(context.Background(), root, repositories, "missing", "abcdef0"); err == nil {
		t.Fatal("missing commit repository accepted")
	}
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	log, err := runnerCollectWorkspaceLog(context.Background(), root, []domain.RepositoryContext{{WorkspaceDir: "broken"}}, "broken")
	if err != nil || len(log["commits"].([]runnerWorkspaceCommit)) != 0 {
		t.Fatalf("broken log=%v err=%v", log, err)
	}
}

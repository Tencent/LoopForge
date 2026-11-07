package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestWorkspaceResolutionAndRepositoryErrorBranches(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "dir"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600)
	_ = os.Symlink(filepath.Join(root, "file"), filepath.Join(root, "link"))
	if _, _, err := runnerResolveWorkspacePath(root, "file", true); err == nil {
		t.Fatal("file accepted as directory")
	}
	if _, _, err := runnerResolveWorkspacePath(root, "dir", false); err == nil {
		t.Fatal("directory accepted as file")
	}
	if _, _, err := runnerResolveWorkspacePath(root, "link", false); err == nil {
		t.Fatal("symlink accepted")
	}
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}}
	if _, err := d.runnerWorkspaceRoot(domain.RunnerWorkspaceRequest{RunID: ""}); err == nil {
		t.Fatal("empty run accepted")
	}
	if _, err := d.runnerWorkspaceRoot(domain.RunnerWorkspaceRequest{RunID: "run", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, WorkspaceRunID: "../bad"}); err == nil {
		t.Fatal("bad owner accepted")
	}
	if response := runnerWorkspaceError(os.ErrNotExist); response.Status != 404 {
		t.Fatalf("notfound=%+v", response)
	}
	repositories := []domain.RepositoryContext{{WorkspaceDir: "../bad"}, {WorkspaceDir: "repo"}}
	roots := runnerWorkspaceRepositoryRoots(root, repositories)
	if len(roots) != 1 || roots[0].label != "repo" {
		t.Fatalf("roots=%+v", roots)
	}
	if _, _, ok := runnerFindWorkspaceRepository(root, []domain.RepositoryContext{{WorkspaceDir: "a"}, {WorkspaceDir: "b"}}, ""); ok {
		t.Fatal("ambiguous default repository found")
	}
	if _, err := runnerCollectWorkspaceLog(context.Background(), root, repositories, "missing"); err == nil {
		t.Fatal("missing log repository accepted")
	}
	log, err := runnerCollectWorkspaceLog(context.Background(), root, repositories, "repo")
	if err != nil || log["branch"] != "" {
		t.Fatalf("nongit log=%v err=%v", log, err)
	}
	if _, err := runnerCollectWorkspaceCommitFiles(context.Background(), root, repositories, "repo", "invalid"); err == nil {
		t.Fatal("invalid commit accepted")
	}
	if files, err := runnerCollectWorkspaceCommitFiles(context.Background(), root, repositories, "repo", "abcdef0"); err != nil || len(files) != 0 {
		t.Fatalf("nongit files=%v err=%v", files, err)
	}
}

func TestWorkspaceGitBlobAndBaseEdgeBranches(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(repo, 0o755)
	cvRGit(t, repo, "init", "-q")
	cvRGit(t, repo, "config", "user.name", "cvR")
	cvRGit(t, repo, "config", "user.email", "cvr@example.test")
	_ = os.WriteFile(filepath.Join(repo, "binary"), []byte{0, 1}, 0o600)
	_ = os.WriteFile(filepath.Join(repo, "large"), make([]byte, runnerWorkspaceFileLimit+10), 0o600)
	cvRGit(t, repo, "add", "binary", "large")
	cvRGit(t, repo, "commit", "-qm", "files")
	head := strings.TrimSpace(cvRGit(t, repo, "rev-parse", "HEAD"))
	repositories := []domain.RepositoryContext{{WorkspaceDir: "repo"}}
	if _, _, err := runnerReadWorkspaceGitBlob(context.Background(), root, repositories, "repo/binary", head); err == nil {
		t.Fatal("binary blob accepted")
	}
	body, size, err := runnerReadWorkspaceGitBlob(context.Background(), root, repositories, "repo/large", head)
	if err != nil || len(body) != runnerWorkspaceFileLimit || size != runnerWorkspaceFileLimit {
		t.Fatalf("large len=%d size=%d err=%v", len(body), size, err)
	}
	if body, size, err := runnerReadWorkspaceGitBlob(context.Background(), root, repositories, "repo/missing", head); err != nil || body != "" || size != 0 {
		t.Fatalf("missing=%q size=%d err=%v", body, size, err)
	}
	if _, _, err := runnerReadWorkspaceGitBlob(context.Background(), root, repositories, "../bad", head); err == nil {
		t.Fatal("unsafe blob path accepted")
	}
	if _, _, err := runnerReadWorkspaceGitBlob(context.Background(), root, repositories, "repo/large", "bad"); err == nil {
		t.Fatal("bad blob ref accepted")
	}
	_ = os.WriteFile(filepath.Join(repo, "untracked"), []byte("new"), 0o600)
	base, truncated, err := runnerReadWorkspaceBaseFile(context.Background(), root, repositories, "repo/untracked")
	if err != nil || base != "" || truncated {
		t.Fatalf("untracked base=%q truncated=%v err=%v", base, truncated, err)
	}
}

func TestWorkspaceGitFailureAndNoRepositoryDiff(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "broken")
	_ = os.MkdirAll(filepath.Join(broken, ".git"), 0o755)
	repositories := []domain.RepositoryContext{{WorkspaceDir: "broken"}}
	_, warning := runnerCollectWorkspaceChanges(context.Background(), root, repositories)
	if warning == "" {
		t.Fatal("broken git produced no warning")
	}
	diff, truncated, err := runnerReadWorkspaceDiff(context.Background(), root, nil, "")
	if err != nil || diff != "" || truncated {
		t.Fatalf("nongit diff=%q truncated=%v err=%v", diff, truncated, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := runnerReadWorkspaceDiff(ctx, root, repositories, ""); err == nil {
		t.Fatal("canceled diff accepted")
	}
}

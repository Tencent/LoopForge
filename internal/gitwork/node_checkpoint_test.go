package gitwork

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeCheckpointCapturesWorktreeWithoutChangingGitState(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write(".gitignore", "ignored\n")
	write("modified", "original")
	write("deleted", "original")
	git("add", "-A")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "baseline")
	write("modified", "staged")
	git("add", "modified")
	write("modified", "worktree")
	if err := os.Remove(filepath.Join(dir, "deleted")); err != nil {
		t.Fatal(err)
	}
	write("new file", "new")
	write("ignored", "ignored")
	beforeHead := git("rev-parse", "HEAD")
	beforeBranch := git("symbolic-ref", "HEAD")
	beforeStatus := git("status", "--porcelain=v1", "--untracked-files=all")
	indexPath := git("rev-parse", "--git-path", "index")
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(dir, indexPath)
	}
	beforeIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := CreateNodeCheckpoint(context.Background(), dir, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Head != beforeHead || "refs/heads/"+checkpoint.Branch != beforeBranch || checkpoint.FileCount != 3 {
		t.Fatalf("unexpected checkpoint: %+v", checkpoint)
	}
	if git("rev-parse", checkpoint.Ref) != checkpoint.Commit || git("rev-parse", checkpoint.Commit+"^") != beforeHead {
		t.Fatal("checkpoint ref or parent is incorrect")
	}
	if git("show", checkpoint.Commit+":modified") != "worktree" || git("show", checkpoint.Commit+":new file") != "new" {
		t.Fatal("checkpoint does not contain working tree content")
	}
	files := git("ls-tree", "-r", "--name-only", checkpoint.Commit)
	if strings.Contains(files, "deleted") || strings.Contains(files, "ignored") {
		t.Fatalf("deleted or ignored file was captured: %s", files)
	}
	afterIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if git("rev-parse", "HEAD") != beforeHead || git("symbolic-ref", "HEAD") != beforeBranch || git("status", "--porcelain=v1", "--untracked-files=all") != beforeStatus || !bytes.Equal(beforeIndex, afterIndex) {
		t.Fatal("checkpoint changed HEAD, branch, worktree status or index")
	}
	if _, err := CreateNodeCheckpoint(context.Background(), dir, "0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("checkpoint ref was overwritten")
	}
	if git("rev-parse", checkpoint.Ref) != checkpoint.Commit {
		t.Fatal("existing checkpoint changed")
	}
}

func TestNodeCheckpointValidatesIDAndHandlesDetachedHead(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "baseline")
	git("checkout", "-q", "--detach")
	if _, err := CreateNodeCheckpoint(context.Background(), dir, "../../heads/main"); err == nil {
		t.Fatal("invalid ID was accepted")
	}
	checkpoint, err := CreateNodeCheckpoint(context.Background(), dir, "abcdef0123456789abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Branch != "" || checkpoint.FileCount != 0 {
		t.Fatalf("unexpected detached checkpoint: %+v", checkpoint)
	}
}

func TestNodeCheckpointRefsAreNotPushedWithWorkBranch(t *testing.T) {
	dir := t.TempDir()
	remote := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	workBranchGit(t, remote, "init", "--bare", "-q")
	git("init", "-q")
	git("checkout", "-qb", "work")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "baseline")
	git("remote", "add", "origin", remote)
	if _, err := CreateNodeCheckpoint(context.Background(), dir, "123456789abcdef0123456789abcdef0"); err != nil {
		t.Fatal(err)
	}
	if _, err := PushWorkBranch(context.Background(), dir, "work", "test"); err != nil {
		t.Fatal(err)
	}
	if refs := workBranchGit(t, remote, "for-each-ref", "--format=%(refname)", "refs/loopforge"); refs != "" {
		t.Fatalf("private checkpoint refs were pushed: %s", refs)
	}
}

func TestNodeCheckpointSplitIndex(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "file")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "baseline")
	git("update-index", "--split-index")
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := CreateNodeCheckpoint(context.Background(), dir, "11111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if git("show", checkpoint.Commit+":file") != "after" {
		t.Fatal("split-index checkout was not captured")
	}
}

func TestNodeCheckpointIncludesFilesHiddenByIndexFlags(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	git("init", "-q")
	for _, name := range []string{"skipped", "assumed"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "baseline")
	git("update-index", "--skip-worktree", "skipped")
	git("update-index", "--assume-unchanged", "assumed")
	for _, name := range []string{"skipped", "assumed"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("after"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := git("ls-files", "-v")
	checkpoint, err := CreateNodeCheckpoint(context.Background(), dir, "22222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"skipped", "assumed"} {
		if git("show", checkpoint.Commit+":"+name) != "after" {
			t.Fatalf("%s was not captured", name)
		}
	}
	if git("ls-files", "-v") != before {
		t.Fatal("original index flags changed")
	}
}

func TestDiffNodeCheckpointsIncludesUntrackedFilesAndTruncates(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "baseline")
	start, err := CreateNodeCheckpoint(context.Background(), dir, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new"), []byte("new content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	end, err := CreateNodeCheckpoint(context.Background(), dir, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	diff, truncated, err := DiffNodeCheckpoints(context.Background(), dir, start.Ref, end.Ref, 4096)
	if err != nil || truncated || !strings.Contains(diff, "+new content") {
		t.Fatalf("unexpected diff %q truncated=%v err=%v", diff, truncated, err)
	}
	diff, truncated, err = DiffNodeCheckpoints(context.Background(), dir, start.Ref, end.Ref, 8)
	if err != nil || !truncated || len(diff) != 8 {
		t.Fatalf("unexpected truncated diff %q truncated=%v err=%v", diff, truncated, err)
	}
	if _, _, err := DiffNodeCheckpoints(context.Background(), dir, "HEAD", end.Ref, 100); err == nil {
		t.Fatal("arbitrary revision accepted")
	}
}

func TestRestoreNodeCheckpointPreservesIgnoredFiles(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write(".gitignore", "ignored\n")
	write("modified", "base")
	write("deleted", "base")
	git("add", "-A")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "baseline")
	write("modified", "start")
	write("existing-untracked", "start")
	checkpoint, err := CreateNodeCheckpoint(context.Background(), dir, "cccccccccccccccccccccccccccccccc")
	if err != nil {
		t.Fatal(err)
	}
	write("modified", "failed")
	write("new", "failed")
	write("ignored", "keep")
	if err := os.Remove(filepath.Join(dir, "deleted")); err != nil {
		t.Fatal(err)
	}
	if err := RestoreNodeCheckpoint(context.Background(), dir, checkpoint); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"modified": "start", "deleted": "base", "existing-untracked": "start", "ignored": "keep"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: got %q, %v", name, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
		t.Fatalf("new file still exists: %v", err)
	}
	if git("rev-parse", "HEAD") != checkpoint.Head {
		t.Fatal("HEAD differs from checkpoint")
	}
}

func TestRestoreNodeCheckpointRejectsIgnoredConflictBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "baseline")
	checkpoint, err := CreateNodeCheckpoint(context.Background(), dir, "dddddddddddddddddddddddddddddddd")
	if err != nil {
		t.Fatal(err)
	}
	git("rm", "-q", "--cached", "file")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("ignored data"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeHead := git("rev-parse", "HEAD")
	if err := RestoreNodeCheckpoint(context.Background(), dir, checkpoint); err == nil || !strings.Contains(err.Error(), "ignored path") {
		t.Fatalf("expected ignored conflict, got %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "file"))
	if err != nil || string(content) != "ignored data" || git("rev-parse", "HEAD") != beforeHead {
		t.Fatal("failed restore modified the workspace")
	}
}

func TestRestoreNodeCheckpointRejectsUnfinishedGitOperation(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "baseline")
	checkpoint, err := CreateNodeCheckpoint(context.Background(), dir, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "MERGE_HEAD"), []byte(checkpoint.Head+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreNodeCheckpoint(context.Background(), dir, checkpoint); err == nil || !strings.Contains(err.Error(), "unfinished Git operation") {
		t.Fatalf("expected unfinished operation error, got %v", err)
	}
}

func TestRestoreNodeCheckpointRejectsChangedHead(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "baseline")
	cp, err := CreateNodeCheckpoint(context.Background(), dir, "ffffffffffffffffffffffffffffffff")
	if err != nil {
		t.Fatal(err)
	}
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "later")
	head := git("rev-parse", "HEAD")
	if err := RestoreNodeCheckpoint(context.Background(), dir, cp); err == nil || !strings.Contains(err.Error(), "HEAD differs") {
		t.Fatalf("expected changed HEAD rejection, got %v", err)
	}
	if git("rev-parse", "HEAD") != head {
		t.Fatal("restore changed HEAD")
	}
}

func TestRestoreNodeCheckpointRejectsDeepNestedRepository(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string { return workBranchGit(t, dir, args...) }
	git("init", "-q")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "baseline")
	cp, err := CreateNodeCheckpoint(context.Background(), dir, "1234567890abcdef1234567890abcdef")
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "untracked", "deep")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	workBranchGit(t, nested, "init", "-q")
	if err := RestoreNodeCheckpoint(context.Background(), dir, cp); err == nil || !strings.Contains(err.Error(), "nested repository") {
		t.Fatalf("expected nested repository rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, ".git")); err != nil {
		t.Fatalf("nested repository changed: %v", err)
	}
}

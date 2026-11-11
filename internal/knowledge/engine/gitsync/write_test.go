package gitsync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedBareRemote creates a bare repository with one commit on main and returns
// its absolute path and a working clone used to change the fixture.
func seedBareRemote(t *testing.T) (remote, work string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "repo.git")
	work = filepath.Join(root, "seed")
	runGit(t, root, "init", "--bare", "-b", "main", remote)
	runGit(t, root, "init", "-b", "main", work)
	runGit(t, work, "config", "user.email", "test@example.com")
	runGit(t, work, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# 样例\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "docs", "old.md"), []byte("# 旧文档\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-m", "initial")
	runGit(t, work, "remote", "add", "origin", remote)
	runGit(t, work, "push", "origin", "main")
	return remote, work
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := tryGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func tryGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestCommitAndPushCreatesBranchAndFile(t *testing.T) {
	ctx := context.Background()
	remote, _ := seedBareRemote(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	result, err := CommitAndPush(ctx, cache, remote, "main", WriteOptions{
		Branch: "kb/writeback-kbs-1", Path: "docs/new.md", Content: "# 新知识\n\n排障结论。\n",
		Message: "docs: add reviewed knowledge", AuthorName: "LoopForge Knowledge", AuthorEmail: "knowledge@loopforge.local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Branch != "kb/writeback-kbs-1" || result.Commit == "" || result.Path != "docs/new.md" {
		t.Fatalf("result = %+v", result)
	}
	// The branch is on the remote and carries the new file.
	if got := runGit(t, remote, "rev-parse", "refs/heads/kb/writeback-kbs-1"); got != result.Commit {
		t.Fatalf("remote branch = %s want %s", got, result.Commit)
	}
	if got := runGit(t, remote, "show", "kb/writeback-kbs-1:docs/new.md"); got != "# 新知识\n\n排障结论。" {
		t.Fatalf("remote content = %q", got)
	}
	// Only the reviewed path is in the commit.
	if got := runGit(t, remote, "diff-tree", "--no-commit-id", "--name-only", "-r", result.Commit); got != "docs/new.md" {
		t.Fatalf("commit touched %q", got)
	}
	// Author is the platform, not the cache's Git identity.
	if got := runGit(t, remote, "log", "-1", "--format=%an <%ae>", result.Commit); got != "LoopForge Knowledge <knowledge@loopforge.local>" {
		t.Fatalf("author = %q", got)
	}
	// The source branch is untouched.
	if _, err := tryGit(remote, "show", "main:docs/new.md"); err == nil {
		t.Fatal("main unexpectedly contains the new file")
	}
}

func TestCommitAndPushUpdatesBranchOnRetry(t *testing.T) {
	ctx := context.Background()
	remote, _ := seedBareRemote(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	write := func(content string) WriteResult {
		t.Helper()
		result, err := CommitAndPush(ctx, cache, remote, "main", WriteOptions{
			Branch: "kb/writeback-kbs-2", Path: "docs/retry.md", Content: content,
			Message: "docs: retry knowledge", AuthorName: "LoopForge Knowledge", AuthorEmail: "knowledge@loopforge.local",
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := write("# 第一版\n")
	second := write("# 第二版\n\n补充说明。\n")
	if second.Commit == first.Commit {
		t.Fatal("retry must create a new commit")
	}
	if got := runGit(t, remote, "show", "kb/writeback-kbs-2:docs/retry.md"); got != "# 第二版\n\n补充说明。" {
		t.Fatalf("content = %q", got)
	}
	// The retry rebases onto the source branch instead of branching twice.
	if got := runGit(t, remote, "rev-list", "--count", "main..kb/writeback-kbs-2"); got != "1" {
		t.Fatalf("branch has %s commits over main, want 1", got)
	}
}

func TestCommitAndPushRejectsUnsafeInput(t *testing.T) {
	ctx := context.Background()
	remote, _ := seedBareRemote(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	base := WriteOptions{Branch: "kb/writeback-safe", Path: "docs/safe.md", Content: "# ok\n", Message: "m", AuthorName: "n", AuthorEmail: "e@example.com"}
	cases := []struct {
		name string
		set  func(*WriteOptions)
	}{
		{"路径逃逸", func(o *WriteOptions) { o.Path = "../escape.md" }},
		{"绝对路径", func(o *WriteOptions) { o.Path = "/etc/passwd.md" }},
		{"非 Markdown", func(o *WriteOptions) { o.Path = "docs/run.sh" }},
		{"空内容", func(o *WriteOptions) { o.Content = "  \n" }},
		{"分支等于来源分支", func(o *WriteOptions) { o.Branch = "main" }},
		{"非法分支", func(o *WriteOptions) { o.Branch = "kb/../x" }},
		{"ref 前缀分支", func(o *WriteOptions) { o.Branch = "refs/heads/x" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := base
			tc.set(&opts)
			if _, err := CommitAndPush(ctx, cache, remote, "main", opts); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestCommitAndPushRecoversDirtyCache(t *testing.T) {
	ctx := context.Background()
	remote, _ := seedBareRemote(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	// A crashed writer may leave a tracked file modified or an untracked file.
	if err := os.WriteFile(filepath.Join(cache, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "untracked.md"), []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitAndPush(ctx, cache, remote, "main", WriteOptions{
		Branch: "kb/writeback-dirty", Path: "docs/clean.md", Content: "# ok\n",
		Message: "m", AuthorName: "n", AuthorEmail: "e@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "untracked.md")); !os.IsNotExist(err) {
		t.Fatalf("untracked cache file survived cleanup: %v", err)
	}
}

func TestCommitAndPushRejectsSymlinkTarget(t *testing.T) {
	ctx := context.Background()
	remote, _ := seedBareRemote(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("old.md", filepath.Join(cache, "docs", "evil.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Track the symlink on the source branch: an untracked leftover is cache
	// debris and is deliberately cleaned, but a committed link must be refused.
	runGit(t, cache, "add", "docs/evil.md")
	runGit(t, cache, "commit", "-m", "track symlink")
	runGit(t, cache, "push", "origin", "HEAD:main")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitAndPush(ctx, cache, remote, "main", WriteOptions{
		Branch: "kb/writeback-evil", Path: "docs/evil.md", Content: "# replaced\n",
		Message: "m", AuthorName: "n", AuthorEmail: "e@example.com",
	}); err == nil {
		t.Fatal("writing through a symlink must be rejected")
	}
	body, err := os.ReadFile(filepath.Join(cache, "docs", "old.md"))
	if err != nil || string(body) != "# 旧文档\n" {
		t.Fatalf("symlink target was modified: %q %v", body, err)
	}
}

func TestRemoteContainsCommit(t *testing.T) {
	ctx := context.Background()
	remote, _ := seedBareRemote(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	result, err := CommitAndPush(ctx, cache, remote, "main", WriteOptions{
		Branch: "kb/writeback-merged", Path: "docs/merged.md", Content: "# 已合并\n",
		Message: "m", AuthorName: "n", AuthorEmail: "e@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	contains, err := RemoteContainsCommit(ctx, cache, remote, "main", result.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if contains {
		t.Fatal("branch commit is not on main before merge")
	}
	runGit(t, cache, "checkout", "-B", "main", "origin/main")
	runGit(t, cache, "merge", "--no-ff", "-m", "merge knowledge", "kb/writeback-merged")
	runGit(t, cache, "push", "origin", "main")
	contains, err = RemoteContainsCommit(ctx, cache, remote, "main", result.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if !contains {
		t.Fatal("merged commit must be detected on main")
	}
}

func TestCommitAndPushRejectionIsUserFacing(t *testing.T) {
	ctx := context.Background()
	remote, _ := seedBareRemote(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'protected branch: secret-canary-token' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := CommitAndPush(ctx, cache, remote, "main", WriteOptions{
		Branch: "kb/writeback-protected", Path: "docs/denied.md", Content: "# denied\n",
		Message: "m", AuthorName: "n", AuthorEmail: "e@example.com",
	})
	if err == nil {
		t.Fatal("push must fail")
	}
	if strings.Contains(err.Error(), "secret-canary-token") || strings.Contains(err.Error(), "protected branch") {
		t.Fatalf("error leaked remote output: %v", err)
	}
	if !strings.Contains(err.Error(), "推送") {
		t.Fatalf("error should be user-facing Chinese: %v", err)
	}
}

func TestCommitAndPushReportsNoChanges(t *testing.T) {
	ctx := context.Background()
	remote, _ := seedBareRemote(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	_, err := CommitAndPush(ctx, cache, remote, "main", WriteOptions{
		Branch: "kb/writeback-nochange", Path: "docs/old.md",
		Content: "# 旧文档\n", Message: "m", AuthorName: "n", AuthorEmail: "e@example.com",
	})
	if !errors.Is(err, ErrNoWriteChanges) {
		t.Fatalf("err = %v, want ErrNoWriteChanges", err)
	}
	if _, showErr := tryGit(remote, "show", "kb/writeback-nochange:docs/old.md"); showErr == nil {
		t.Fatal("no-op writeback created a branch")
	}
}

func TestCommitAndPushRefusesPeerBranchUpdate(t *testing.T) {
	ctx := context.Background()
	remote, _ := seedBareRemote(t)
	cache := filepath.Join(t.TempDir(), "cache")
	if _, _, _, _, _, err := Sync(ctx, cache, remote, "main", nil); err != nil {
		t.Fatal(err)
	}
	first, err := CommitAndPush(ctx, cache, remote, "main", WriteOptions{
		Branch: "kb/writeback-peer", Path: "docs/peer.md", Content: "# 第一版\n",
		Message: "m", AuthorName: "n", AuthorEmail: "e@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A peer clone moves the branch on the remote after our lease was lost.
	peer := filepath.Join(t.TempDir(), "peer")
	runGit(t, filepath.Dir(peer), "clone", "--quiet", "--branch", "kb/writeback-peer", remote, peer)
	runGit(t, peer, "config", "user.email", "peer@example.com")
	runGit(t, peer, "config", "user.name", "peer")
	if err := os.WriteFile(filepath.Join(peer, "docs", "peer.md"), []byte("# 对方版本\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, peer, "add", "-A")
	runGit(t, peer, "commit", "-m", "peer edit")
	runGit(t, peer, "push", "origin", "HEAD:kb/writeback-peer")
	_, err = CommitAndPush(ctx, cache, remote, "main", WriteOptions{
		Branch: "kb/writeback-peer", Path: "docs/peer.md", Content: "# 我的版本\n",
		Message: "m", AuthorName: "n", AuthorEmail: "e@example.com", ExpectedRemote: first.Commit,
	})
	if err == nil || !strings.Contains(err.Error(), "其他写入更新") {
		t.Fatalf("err = %v", err)
	}
	if got := runGit(t, remote, "show", "kb/writeback-peer:docs/peer.md"); got != "# 对方版本" {
		t.Fatalf("peer content was overwritten: %q", got)
	}
}

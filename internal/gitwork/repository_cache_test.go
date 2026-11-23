package gitwork

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/procwatch"
)

func cacheTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func cacheTestCommit(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheTestGit(t, dir, "add", name)
	cacheTestGit(t, dir, "commit", "-q", "-m", name)
	return cacheTestGit(t, dir, "rev-parse", "HEAD")
}

func TestRepositoryCachePathRejectsUnsafeIDs(t *testing.T) {
	root := t.TempDir()
	if got := RepositoryCachePath(root, "service"); got != filepath.Join(root, "service.git") {
		t.Fatalf("cache path = %q", got)
	}
	for _, id := range []string{"", "../escape", "a/b", ".hidden", "a..b", "-flag"} {
		if got := RepositoryCachePath(root, id); got != "" {
			t.Fatalf("%q accepted as %q", id, got)
		}
	}
	if got := RepositoryCachePath("", "service"); got != "" {
		t.Fatalf("empty root accepted as %q", got)
	}
}

func TestCloneFromRepositoryCacheSharesObjectsAndKeepsOrigin(t *testing.T) {
	origin := t.TempDir()
	cacheTestGit(t, origin, "init", "-q", "-b", "main")
	first := cacheTestCommit(t, origin, "a.txt")
	cacheTestGit(t, origin, "checkout", "-q", "-b", "feature")
	feature := cacheTestCommit(t, origin, "b.txt")
	cacheTestGit(t, origin, "checkout", "-q", "main")
	remote := "file://" + origin
	root := filepath.Join(t.TempDir(), "cache")
	ctx := context.Background()

	workspace := t.TempDir()
	if err := CloneFromRepositoryCache(ctx, root, "service", remote, "", workspace, procwatch.Options{}); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "service.git")
	if got := cacheTestGit(t, cache, "config", "gc.auto"); got != "0" {
		t.Fatalf("cache gc.auto = %q", got)
	}
	if head := cacheTestGit(t, workspace, "rev-parse", "HEAD"); head != first {
		t.Fatalf("clone HEAD = %s, want %s", head, first)
	}
	if url := cacheTestGit(t, workspace, "remote", "get-url", "origin"); url != remote {
		t.Fatalf("clone origin = %q, want the repository remote", url)
	}
	alternates, err := os.ReadFile(filepath.Join(workspace, ".git", "objects", "info", "alternates"))
	if err != nil || !strings.Contains(string(alternates), cache) {
		t.Fatalf("clone does not borrow the cache objects: %q %v", alternates, err)
	}

	next := cacheTestCommit(t, origin, "c.txt")
	branchWorkspace := t.TempDir()
	if err := CloneFromRepositoryCache(ctx, root, "service", remote, "feature", branchWorkspace, procwatch.Options{}); err != nil {
		t.Fatal(err)
	}
	if head := cacheTestGit(t, branchWorkspace, "rev-parse", "HEAD"); head != feature {
		t.Fatalf("branch clone HEAD = %s, want %s", head, feature)
	}
	if got := cacheTestGit(t, cache, "rev-parse", "refs/heads/main"); got != next {
		t.Fatalf("cache main = %s, want the fetched %s", got, next)
	}
}

func TestRefreshRepositoryCacheReplacesABrokenCache(t *testing.T) {
	origin := t.TempDir()
	cacheTestGit(t, origin, "init", "-q", "-b", "main")
	head := cacheTestCommit(t, origin, "a.txt")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "service.git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service.git", "junk"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := RefreshRepositoryCache(context.Background(), root, "service", "file://"+origin, procwatch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cacheTestGit(t, dir, "rev-parse", "refs/heads/main"); got != head {
		t.Fatalf("cache main = %s, want %s", got, head)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk")); !os.IsNotExist(err) {
		t.Fatalf("broken cache content kept: %v", err)
	}
	if _, err := RefreshRepositoryCache(context.Background(), root, "../x", "file://"+origin, procwatch.Options{}); err == nil {
		t.Fatal("unsafe repository ID accepted")
	}
}

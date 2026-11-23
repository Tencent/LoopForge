package gitwork

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestCloneFromRepositoryCacheKeepsAnIndependentCheckout(t *testing.T) {
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, []byte("[safe]\n\tbareRepository = explicit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
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
	if err := CloneFromRepositoryCache(ctx, root, "service", remote, "main", workspace, true, procwatch.Options{}); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "service.git")
	if got := cacheTestGit(t, cache, "--git-dir=.", "config", "gc.auto"); got != "0" {
		t.Fatalf("cache gc.auto = %q", got)
	}
	if head := cacheTestGit(t, workspace, "rev-parse", "HEAD"); head != first {
		t.Fatalf("clone HEAD = %s, want %s", head, first)
	}
	if url := cacheTestGit(t, workspace, "remote", "get-url", "origin"); url != remote {
		t.Fatalf("clone origin = %q, want the repository remote", url)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("checkout should have its own objects: %v", err)
	}

	next := cacheTestCommit(t, origin, "c.txt")
	branchWorkspace := t.TempDir()
	if err := CloneFromRepositoryCache(ctx, root, "service", remote, "feature", branchWorkspace, true, procwatch.Options{}); err != nil {
		t.Fatal(err)
	}
	if head := cacheTestGit(t, branchWorkspace, "rev-parse", "HEAD"); head != feature {
		t.Fatalf("branch clone HEAD = %s, want %s", head, feature)
	}
	if got := cacheTestGit(t, cache, "--git-dir=.", "rev-parse", "refs/heads/main"); got != next {
		t.Fatalf("cache main = %s, want the fetched %s", got, next)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err := CloneFromRepositoryCache(cancelled, root, "service", remote, "main", t.TempDir(), false, procwatch.Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled checkout error = %v", err)
	}
	if got := cacheTestGit(t, cache, "--git-dir=.", "rev-parse", "refs/heads/main"); got != next {
		t.Fatalf("cancelled checkout changed the cache: %q", got)
	}

	// The checkout can be used on a different machine without this cache.
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	cacheTestGit(t, workspace, "fsck", "--full")
	cacheTestGit(t, branchWorkspace, "fsck", "--full")
}

func TestCloneFromRepositoryCacheReplacesABrokenCache(t *testing.T) {
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
	dir := RepositoryCachePath(root, "service")
	if err := CloneFromRepositoryCache(context.Background(), root, "service", "file://"+origin, "main", t.TempDir(), false, procwatch.Options{}); err != nil {
		t.Fatal(err)
	}
	if got := cacheTestGit(t, dir, "--git-dir=.", "rev-parse", "refs/heads/main"); got != head {
		t.Fatalf("cache main = %s, want %s", got, head)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk")); !os.IsNotExist(err) {
		t.Fatalf("broken cache content kept: %v", err)
	}
	workspace := t.TempDir()
	file := filepath.Join(workspace, "notes")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CloneFromRepositoryCache(context.Background(), root, "service", "file://"+origin, "main", workspace, false, procwatch.Options{}); err == nil {
		t.Fatal("nonempty workspace accepted for clone")
	}
	if content, err := os.ReadFile(file); err != nil || string(content) != "keep" {
		t.Fatalf("existing workspace file was changed: %q %v", content, err)
	}
}

func TestRepositoryCacheRefreshFollowsRemoteDefaultBranch(t *testing.T) {
	ctx := context.Background()
	root, origin := t.TempDir(), t.TempDir()
	cacheTestGit(t, origin, "init", "-q", "-b", "previous")
	cacheTestCommit(t, origin, "a.txt")
	remote := "file://" + origin
	clone := func(workspace string) {
		t.Helper()
		if err := CloneFromRepositoryCache(ctx, root, "service", remote, "previous", workspace, false, procwatch.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	clone(t.TempDir())
	cacheTestGit(t, origin, "checkout", "-q", "-b", "new-default")
	workspace := t.TempDir()
	clone(workspace)
	if head := cacheTestGit(t, workspace, "symbolic-ref", "refs/remotes/origin/HEAD"); head != "refs/remotes/origin/new-default" {
		t.Fatalf("origin/HEAD = %q, want the new default", head)
	}
}

func TestRepositoryCacheCheckoutUsesOneTimeout(t *testing.T) {
	ctx := context.Background()
	root, origin := t.TempDir(), t.TempDir()
	cacheTestGit(t, origin, "init", "-q", "-b", "main")
	cacheTestCommit(t, origin, "a.txt")
	remote := "file://" + origin
	if err := CloneFromRepositoryCache(ctx, root, "service", remote, "main", t.TempDir(), false, procwatch.Options{}); err != nil {
		t.Fatal(err)
	}
	t.Run("waiting for lock", func(t *testing.T) {
		unlock, err := lockRepositoryCache(ctx, RepositoryCachePath(root, "service"))
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		timed, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err = CloneFromRepositoryCache(timed, root, "service", remote, "main", t.TempDir(), false, procwatch.Options{
			Timeout: 200 * time.Millisecond, TimeoutEnv: CloneTimeoutEnv,
		})
		if !procwatch.Stopped(err) || !strings.Contains(err.Error(), CloneTimeoutEnv) {
			t.Fatalf("cache lock exceeded the clone budget: %v", err)
		}
	})
	t.Run("copying from cache", func(t *testing.T) {
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bin, marker := t.TempDir(), filepath.Join(t.TempDir(), "local-clone")
		script := `#!/bin/sh
case "$*" in
  *"clone --progress --no-local --branch"*) touch "$LOOPFORGE_TEST_CACHE_CLONE"; exec sleep 8 ;;
esac
exec "$LOOPFORGE_TEST_REAL_GIT" "$@"
`
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("LOOPFORGE_TEST_CACHE_CLONE", marker)
		t.Setenv("LOOPFORGE_TEST_REAL_GIT", git)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		err = CloneFromRepositoryCache(ctx, root, "service", remote, "main", t.TempDir(), false, procwatch.Options{
			Timeout: 2 * time.Second, TimeoutEnv: CloneTimeoutEnv,
		})
		if !procwatch.Stopped(err) || !strings.Contains(err.Error(), CloneTimeoutEnv) {
			t.Fatalf("local clone exceeded the clone budget: %v", err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("local clone did not start: %v", err)
		}
	})
}

package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
)

func TestWithRepositoryCacheForOnlyNativeDrivers(t *testing.T) {
	previous := RepositoryCacheRoot()
	t.Cleanup(func() { repositoryCacheRoot.Store(previous) })
	SetRepositoryCacheRoot(t.TempDir())
	root := RepositoryCacheRoot()
	if filepath.Base(root) != RepositoryCacheDir {
		t.Fatalf("cache root = %q", root)
	}
	for driver, want := range map[string]string{
		domain.SandboxDriverNative: root, domain.SandboxDriverSeatbelt: root, domain.SandboxDriverBwrap: root,
		domain.SandboxDriverDocker: "", domain.SandboxDriverLocal: "", "": "",
	} {
		if got := gitwork.RepositoryCacheFrom(WithRepositoryCacheFor(context.Background(), driver)); got != want {
			t.Fatalf("%q workspace cache = %q, want %q", driver, got, want)
		}
	}
}

func repositoryCacheTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A checkpoint must restore on a machine without this cache, so the shared
// clones in a workspace take their borrowed objects along first.
func TestDissociateSharedClonesCopiesBorrowedObjects(t *testing.T) {
	origin := t.TempDir()
	repositoryCacheTestGit(t, origin, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	repositoryCacheTestGit(t, origin, "add", "a.txt")
	repositoryCacheTestGit(t, origin, "commit", "-q", "-m", "a")
	head := repositoryCacheTestGit(t, origin, "rev-parse", "HEAD")
	cache, workspace := t.TempDir(), t.TempDir()
	checkout := filepath.Join(workspace, "service")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := gitwork.CloneFromRepositoryCache(context.Background(), cache, "service", origin, "", checkout, procwatch.Options{}); err != nil {
		t.Fatal(err)
	}
	if err := dissociateSharedClones(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(checkout, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("alternates kept: %v", err)
	}
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	if got := repositoryCacheTestGit(t, checkout, "rev-parse", "HEAD^{tree}"); got == "" {
		t.Fatal("checkout lost its objects")
	}
	repositoryCacheTestGit(t, checkout, "cat-file", "-e", head)
}

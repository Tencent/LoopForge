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
)

func TestWithRepositoryCacheForOnlyNativeDrivers(t *testing.T) {
	previous := RepositoryCacheRoot()
	t.Cleanup(func() { repositoryCacheRoot.Store(previous) })
	if err := SetRepositoryCacheRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	root := RepositoryCacheRoot()
	if !strings.HasPrefix(root, "/tmp/.loopforge-repository-cache-") {
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

// Seatbelt allows ordinary host temporary files, but Git caches are host-only.
func TestSeatbeltHidesRepositoryCache(t *testing.T) {
	binary, err := exec.LookPath("sandbox-exec")
	if err != nil {
		t.Skip("seatbelt unavailable")
	}
	previous := RepositoryCacheRoot()
	t.Cleanup(func() { repositoryCacheRoot.Store(previous) })
	workspace := t.TempDir()
	if err := SetRepositoryCacheRoot(workspace); err != nil {
		t.Fatal(err)
	}
	root := RepositoryCacheRoot()
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	fixture := filepath.Join(root, "fixture")
	if err := os.WriteFile(fixture, []byte("repository cache fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := seatbeltProfileCommand(workspace, SandboxRequirements{})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := exec.Command(binary, "-p", profile, "/bin/cat", "/dev/null").CombinedOutput(); err != nil {
		t.Fatalf("seatbelt profile does not start: %v (%s)", err, result)
	}
	if err := exec.Command(binary, "-p", profile, "/bin/cat", fixture).Run(); err == nil {
		t.Fatal("sandbox could read host repository cache")
	}
}

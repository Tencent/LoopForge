package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func stubBunFSMountPoint(t *testing.T, path string) string {
	t.Helper()
	previous := bunFSMountPoint
	bunFSMountPoint = path
	t.Cleanup(func() { bunFSMountPoint = previous })
	return path
}

func TestBwrapBunFSMountPoint(t *testing.T) {
	workspace := t.TempDir()
	claude := ProviderRequirements("claude-code")

	// Missing but creatable: created once, then mounted.
	created := stubBunFSMountPoint(t, filepath.Join(t.TempDir(), "$bunfs"))
	args, err := bwrapArgsRequirements(workspace, claude)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--tmpfs "+created) {
		t.Fatalf("creatable mount point not mounted: %v", args)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("mount point not created: %v", err)
	}

	// Cannot be created (parent missing / read-only root): skip the mount
	// instead of failing the whole sandbox.
	missing := stubBunFSMountPoint(t, filepath.Join(t.TempDir(), "absent", "$bunfs"))
	args, err = bwrapArgsRequirements(workspace, claude)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), missing) {
		t.Fatalf("uncreatable mount point must be skipped: %v", args)
	}

	// CLIs without BunFS never touch it.
	untouched := stubBunFSMountPoint(t, filepath.Join(t.TempDir(), "$bunfs"))
	if _, err := bwrapArgsRequirements(workspace, ProviderRequirements("codex")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(untouched); !os.IsNotExist(err) {
		t.Fatalf("codex created the BunFS mount point: %v", err)
	}
}

func TestNativeSandboxResolvesPerOS(t *testing.T) {
	for _, tc := range []struct{ driver, goos, want string }{
		{"native", "darwin", "seatbelt"},
		{"native", "linux", "bwrap"},
		{" native ", "Linux", "bwrap"},
		{"native", "", "native"},
		{"docker", "linux", "docker"},
		{"local", "darwin", "local"},
		{"bwrap", "darwin", "bwrap"},
	} {
		got, err := domain.ResolveSandboxDriver(tc.driver, tc.goos)
		if err != nil || got != tc.want {
			t.Fatalf("ResolveSandboxDriver(%q, %q) = %q, %v; want %q", tc.driver, tc.goos, got, err, tc.want)
		}
	}
	if _, err := domain.ResolveSandboxDriver("native", "windows"); err == nil {
		t.Fatal("native on windows must fail")
	}
}

func TestOpenRemoteSandboxResolvesNative(t *testing.T) {
	// On the test host native must become the host's lightweight sandbox,
	// never reach the driver switch as "native".
	_, err := OpenRemoteSandbox(t.Context(), "native", t.TempDir(), domain.SandboxProfile{ID: "n", Driver: "native"}, SandboxRequirements{}, SandboxResources{})
	if err != nil && strings.Contains(err.Error(), "unsupported runner sandbox driver") {
		t.Fatalf("native leaked to the driver switch: %v", err)
	}
}

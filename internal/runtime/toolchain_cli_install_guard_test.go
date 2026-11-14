//go:build !windows

package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

// Launchers of other packages and plain files that sort before the requested
// launcher are skipped, not taken as the end of the scan.
func TestLinkToolchainLaunchersSkipsEarlierUnrelatedEntries(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	if err := os.Symlink("../lib/node_modules/otherpkg/bin/other.js", filepath.Join(prefix, "bin", "aaa-other")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "aab-plain"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := linkToolchainLaunchers(dir, "fakepkg"); err != nil {
		t.Fatalf("linkToolchainLaunchers() = %v", err)
	}
	if link, err := os.Readlink(filepath.Join(dir, "bin", "fakecli")); err != nil || link != "../lib/node_modules/fakepkg/bin/fake.js" {
		t.Fatalf("launcher fakecli = %q, %v; want the fakepkg link", link, err)
	}
	for _, name := range []string{"aaa-other", "aab-plain"} {
		if _, err := os.Lstat(filepath.Join(dir, "bin", name)); !os.IsNotExist(err) {
			t.Errorf("unrelated entry %s linked: %v", name, err)
		}
	}
}

// A launcher that cannot be linked is reported, not silently dropped.
func TestLinkToolchainLaunchersReportsLinkFailure(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	// dir/bin does not exist, so the symlink cannot be created.
	dir := t.TempDir()
	if err := linkToolchainLaunchers(dir, "fakepkg"); err == nil {
		t.Fatal("linkToolchainLaunchers() = nil, want the symlink error")
	}
}

// EnsureToolchainCLI surfaces a launcher link failure to its caller instead of
// reporting the CLI as installed.
func TestEnsureToolchainCLIReportsLauncherLinkFailure(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	if err := os.Symlink("../lib/node_modules/fakepkg/bin/fake.js", filepath.Join(prefix, "bin", "fakecli-alt")); err != nil {
		t.Fatal(err)
	}
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = (%q, %v)", dir, err)
	}
	// A non-empty directory at the second launcher path can be neither removed
	// nor replaced by a symlink.
	if err := os.MkdirAll(filepath.Join(dir, "bin", "fakecli-alt", "busy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureToolchainCLI(dir, "fakecli"); err == nil {
		t.Fatal("EnsureToolchainCLI() = nil, want the launcher link error")
	}
}

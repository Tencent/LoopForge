package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkToolchainLaunchers links only launchers of the requested package and
// replaces a stale file already sitting at a launcher path.
func TestLinkToolchainLaunchersLinksOnlyThatPackage(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	// A second launcher for fakepkg and a plain (non-symlink) file in bin.
	if err := os.Symlink("../lib/node_modules/fakepkg/bin/fake.js", filepath.Join(prefix, "bin", "fakecli-alt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "plain"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "bin", "fakecli")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := linkToolchainLaunchers(dir, "fakepkg"); err != nil {
		t.Fatalf("linkToolchainLaunchers() = %v", err)
	}
	for _, name := range []string{"fakecli", "fakecli-alt"} {
		link, err := os.Readlink(filepath.Join(dir, "bin", name))
		if err != nil || link != "../lib/node_modules/fakepkg/bin/fake.js" {
			t.Errorf("launcher %s = %q, %v; want the fakepkg link", name, link, err)
		}
	}
	for _, name := range []string{"npm", "npx", "node", "plain"} {
		if _, err := os.Lstat(filepath.Join(dir, "bin", name)); !os.IsNotExist(err) {
			t.Errorf("unrelated entry %s linked: %v", name, err)
		}
	}
}

func TestLinkToolchainLaunchersReportsMissingPrefixBin(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	if err := os.RemoveAll(filepath.Join(prefix, "bin")); err != nil {
		t.Fatal(err)
	}
	if err := linkToolchainLaunchers(t.TempDir(), "fakepkg"); err == nil {
		t.Fatal("linkToolchainLaunchers() = nil, want the read error")
	}
}

// installToolchainPackage replaces an earlier copy and leaves no stage behind.
func TestInstallToolchainPackageReplacesEarlierCopy(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	dir := t.TempDir()
	dest := filepath.Join(dir, "lib", "node_modules", "fakepkg")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dest, "old")
	if err := os.WriteFile(old, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installToolchainPackage(dir, "fakecli", "fakepkg"); err != nil {
		t.Fatalf("installToolchainPackage() = %v", err)
	}
	if !fileIsExecutable(filepath.Join(dest, "bin", "fake.js")) {
		t.Fatal("package not copied into the toolchain")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("earlier copy kept: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(dir, "lib", "node_modules", ".stage-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("leftover stages %v (%v)", stages, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "bin")); !os.IsNotExist(err) {
		t.Fatalf("install touched launchers: %v", err)
	}
}

func TestInstallToolchainPackageNamesBinaryInCopyError(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	dir := t.TempDir()
	err := installToolchainPackage(dir, "gonecli", "@scope/gone")
	if err == nil || !strings.Contains(err.Error(), "复制 gonecli 失败") {
		t.Fatalf("installToolchainPackage() = %v, want the copy error", err)
	}
	stages, globErr := filepath.Glob(filepath.Join(dir, "lib", "node_modules", "@scope", ".stage-*"))
	if globErr != nil || len(stages) != 0 {
		t.Fatalf("leftover stages %v (%v)", stages, globErr)
	}
}

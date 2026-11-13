package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// toolchain_paths_test.go covers the happy paths of the toolchain helpers;
// these cover the error branches and that a published copy is never redone.

func TestRuntimeToolchainDirReportsUnusableCLIDir(t *testing.T) {
	prefix := writeToolchainFixture(t)
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	useToolchainFixture(t, prefix, file)
	if dir, err := RuntimeToolchainDir(); err == nil || dir != "" || !strings.Contains(err.Error(), "准备工具链目录失败") {
		t.Fatalf("RuntimeToolchainDir() = (%q, %v), want the directory error", dir, err)
	}
	sandbox := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: "FROM debian:bookworm\n"}}
	// A sandbox command only uses a published toolchain and never fails on it.
	if env := sandbox.toolchainExecEnv(); env != nil {
		t.Fatalf("toolchainExecEnv() = %v, want none", env)
	}
	if env, err := sandbox.toolchainCLIExecEnv("fakecli"); err == nil || env != nil {
		t.Fatalf("toolchainCLIExecEnv() = (%v, %v), want the error", env, err)
	}
}

func TestEnsureToolchainCLIReportsCopyFailure(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	if err := os.Symlink("../lib/node_modules/gone/bin/gone.js", filepath.Join(prefix, "bin", "gonecli")); err != nil {
		t.Fatal(err)
	}
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = (%q, %v)", dir, err)
	}
	if err := EnsureToolchainCLI(dir, "gonecli"); err == nil || !strings.Contains(err.Error(), "复制 gonecli 失败") {
		t.Fatalf("EnsureToolchainCLI() = %v, want the copy error", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "bin", "gonecli")); !os.IsNotExist(err) {
		t.Fatalf("launcher linked after a failed copy: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(dir, "lib", "node_modules", ".stage-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("leftover stages %v (%v)", stages, err)
	}
}

func TestToolchainPublishedCopiesAreReused(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = (%q, %v)", dir, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("toolchain dir mode = %v, %v; want 0755 so sandbox users can read it", info.Mode().Perm(), err)
	}
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if again, err := RuntimeToolchainDir(); err != nil || again != dir {
		t.Fatalf("second RuntimeToolchainDir() = (%q, %v), want %q", again, err, dir)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("published toolchain was recopied: %v", err)
	}

	if err := EnsureToolchainCLI(dir, "fakecli"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "lib", "node_modules", "fakepkg", "marker")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureToolchainCLI(dir, "fakecli"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("published CLI package was recopied: %v", err)
	}
}

package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestToolchainPackage(t *testing.T) {
	for _, tc := range []struct {
		link   string
		want   string
		wantOK bool
	}{
		{"../lib/node_modules/fakepkg/bin/fake.js", "fakepkg", true},
		{"../lib/node_modules/@scope/multi/bin/a.js", "@scope/multi", true},
		{"../lib/node_modules/@scope/multi", "", false},
		{"../lib/node_modules/fakepkg", "", false},
		{"../lib/node_modules/npm/bin/npm-cli.js", "", false},
		{"/usr/lib/node_modules/fakepkg/bin/fake.js", "", false},
		{"", "", false},
	} {
		got, ok := toolchainPackage(tc.link)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("toolchainPackage(%q) = (%q, %v), want (%q, %v)", tc.link, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestToolchainKeyFallsBackWhenCommitIsUnusable(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	if got := toolchainKey(); got != "testbuild" {
		t.Fatalf("toolchainKey() = %q, want the trimmed commit", got)
	}
	info, err := os.Stat(filepath.Join(prefix, "lib", "node_modules"))
	if err != nil {
		t.Fatal(err)
	}
	mtime := "mtime-" + strconv.FormatInt(info.ModTime().Unix(), 10)
	for _, commit := range []string{"", "  \n", "a/b", `a\b`, "two words"} {
		if err := os.WriteFile(toolchainCommitFile, []byte(commit), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := toolchainKey(); got != mtime {
			t.Errorf("toolchainKey() with commit %q = %q, want %q", commit, got, mtime)
		}
	}
	if err := os.Remove(toolchainCommitFile); err != nil {
		t.Fatal(err)
	}
	if got := toolchainKey(); got != mtime {
		t.Fatalf("toolchainKey() without commit file = %q, want %q", got, mtime)
	}
	toolchainNodePrefix = filepath.Join(prefix, "missing")
	if got := toolchainKey(); got != "default" {
		t.Fatalf("toolchainKey() without commit and node_modules = %q, want default", got)
	}
}

func TestRuntimeToolchainDirDisabledOutsidePlatformImage(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)

	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "  ")
	if dir, err := RuntimeToolchainDir(); dir != "" || err != nil {
		t.Fatalf("without CLI dir = (%q, %v), want empty", dir, err)
	}
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", cliDir)
	if err := os.Chmod(filepath.Join(prefix, "bin", "node"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dir, err := RuntimeToolchainDir(); dir != "" || err != nil {
		t.Fatalf("without executable node = (%q, %v), want empty", dir, err)
	}
	if _, err := os.Stat(filepath.Join(cliDir, "toolchain")); !os.IsNotExist(err) {
		t.Fatalf("toolchain directory created without node: %v", err)
	}
}

func TestRuntimeToolchainDirCopiesBaseToolchainOnce(t *testing.T) {
	prefix := writeToolchainFixture(t)
	cliDir := t.TempDir()
	useToolchainFixture(t, prefix, cliDir)
	python := filepath.Join(prefix, "python")
	if err := os.MkdirAll(filepath.Join(python, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(python, "bin", "python3"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	toolchainPythonDir = python
	parent := filepath.Join(cliDir, "toolchain")
	want := filepath.Join(parent, "testbuild")
	// A copy without .ready was interrupted and must be replaced.
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(want, "stale"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	dir, err := RuntimeToolchainDir()
	if err != nil || dir != want {
		t.Fatalf("RuntimeToolchainDir() = (%q, %v), want %q", dir, err, want)
	}
	for _, name := range []string{"bin/node", "python/bin/python3", "lib/node_modules/npm/bin/npm-cli.js", ".ready"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	for _, name := range []string{"bin/npm", "bin/npx"} {
		if link, err := os.Readlink(filepath.Join(dir, name)); err != nil || link != "../lib/node_modules/npm/bin/npm-cli.js" {
			t.Errorf("%s = (%q, %v), want the npm launcher symlink", name, link, err)
		}
	}
	// Bundled CLIs are copied on demand only; a script node has no ELF loader.
	for _, name := range []string{"stale", ".interp", "bin/fakecli", "lib/node_modules/fakepkg"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should not exist after the base copy: %v", name, err)
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("toolchain dir mode = %v, %v; want 0755 so sandbox users can read it", info.Mode().Perm(), err)
	}

	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := RuntimeToolchainDir()
	if err != nil || again != dir {
		t.Fatalf("second RuntimeToolchainDir() = (%q, %v), want %q", again, err, dir)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("published toolchain was recopied: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") {
			t.Fatalf("leftover stage %s", entry.Name())
		}
	}
}

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

func TestEnsureToolchainCLISkipsUnbundledBinaries(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = (%q, %v)", dir, err)
	}
	for _, tc := range []struct{ dir, binary string }{
		{"", "fakecli"},
		{dir, ""},
		{dir, "../bin/fakecli"},
		{dir, `..\fakecli`},
		{dir, "node"},    // a regular file, not an npm launcher
		{dir, "npm"},     // part of the base copy
		{dir, "missing"}, // not in the platform image
	} {
		if err := EnsureToolchainCLI(tc.dir, tc.binary); err != nil {
			t.Errorf("EnsureToolchainCLI(%q, %q) = %v, want nil", tc.dir, tc.binary, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "lib", "node_modules"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "npm" {
		t.Fatalf("node_modules = %v, want only npm", entries)
	}
	if _, err := os.Lstat(filepath.Join(dir, "bin", "fakecli")); !os.IsNotExist(err) {
		t.Fatalf("fakecli was linked: %v", err)
	}
}

func TestEnsureToolchainCLILinksEveryLauncherOfThePackage(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	pkg := filepath.Join(prefix, "lib", "node_modules", "@scope", "multi", "bin")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.js", "b.js"} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{
		"multi-a": "../lib/node_modules/@scope/multi/bin/a.js",
		"multi-b": "../lib/node_modules/@scope/multi/bin/b.js",
	} {
		if err := os.Symlink(target, filepath.Join(prefix, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = (%q, %v)", dir, err)
	}
	if err := EnsureToolchainCLI(dir, "multi-a"); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"multi-a": "../lib/node_modules/@scope/multi/bin/a.js",
		"multi-b": "../lib/node_modules/@scope/multi/bin/b.js",
	} {
		launcher := filepath.Join(dir, "bin", name)
		if link, err := os.Readlink(launcher); err != nil || link != target {
			t.Errorf("%s = (%q, %v), want %q", name, link, err, target)
		}
		if !fileIsExecutable(launcher) {
			t.Errorf("%s does not resolve to the copied package", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "bin", "fakecli")); !os.IsNotExist(err) {
		t.Fatalf("a launcher of another package was linked: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(dir, "lib", "node_modules", "@scope", ".stage-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("leftover stages %v (%v)", stages, err)
	}

	// Once the launcher exists, a later call keeps the published copy.
	marker := filepath.Join(dir, "lib", "node_modules", "@scope", "multi", "marker")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureToolchainCLI(dir, "multi-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("published package was recopied: %v", err)
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
}

func TestToolchainCLIExecEnvOnlyForCustomImages(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge-runtime:test")

	platform := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	if env, err := platform.toolchainCLIExecEnv("fakecli"); env != nil || err != nil {
		t.Fatalf("platform image toolchainCLIExecEnv() = (%v, %v), want none", env, err)
	}
	if _, err := os.Stat(filepath.Join(RuntimeCLIDir(), "toolchain")); !os.IsNotExist(err) {
		t.Fatalf("platform image sandbox copied the toolchain: %v", err)
	}

	custom := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: "FROM debian:bookworm\n"}}
	dir := filepath.Join(RuntimeCLIDir(), "toolchain", "testbuild")
	want := []string{"-e", "AGENTFLOW_TOOLCHAIN=" + dir}
	env, err := custom.toolchainCLIExecEnv("fakecli")
	if err != nil || !slices.Equal(env, want) {
		t.Fatalf("custom image toolchainCLIExecEnv() = (%v, %v), want %v", env, err, want)
	}
	if !fileIsExecutable(filepath.Join(dir, "bin", "fakecli")) {
		t.Fatal("toolchainCLIExecEnv did not make the CLI available")
	}
}

func TestWithToolchainPathAppendsMountedToolchain(t *testing.T) {
	toolchain := t.TempDir()
	if err := os.WriteFile(filepath.Join(toolchain, ".interp"), []byte("/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	argv := withToolchainPath([]string{"sh", "-c", `printf '%s\n' "$PATH" "$1"`, "sh", "a b"})
	run := func(env ...string) []string {
		t.Helper()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	}
	got := run("AGENTFLOW_TOOLCHAIN=" + toolchain)
	if want := "/usr/bin:/bin:" + toolchain + "/bin:" + toolchain + "/python/bin"; len(got) != 2 || got[0] != want || got[1] != "a b" {
		t.Fatalf("with toolchain = %q, want [%q \"a b\"]", got, want)
	}
	for _, env := range []string{"AGENTFLOW_TOOLCHAIN=", "AGENTFLOW_TOOLCHAIN=" + filepath.Join(toolchain, "missing")} {
		if got := run(env); len(got) != 2 || got[0] != "/usr/bin:/bin" || got[1] != "a b" {
			t.Errorf("%s: got %q, want PATH unchanged", env, got)
		}
	}
	if got := dockerConversationStopArgv("/tmp/pid"); len(got) < 4 || got[0] != "sh" || got[3] != "python3" {
		t.Fatalf("dockerConversationStopArgv() = %q, want python3 behind the toolchain PATH setup", got)
	}
}

func TestElfInterpreter(t *testing.T) {
	sh, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	interp := elfInterpreter(sh)
	if !strings.HasPrefix(interp, "/") || strings.ContainsAny(interp, "\x00\r\n") {
		t.Fatalf("elfInterpreter(%s) = %q, want the dynamic loader path", sh, interp)
	}
	if _, err := os.Stat(interp); err != nil {
		t.Fatalf("loader %q of %s does not exist: %v", interp, sh, err)
	}
	script := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{script, filepath.Join(t.TempDir(), "missing")} {
		if got := elfInterpreter(path); got != "" {
			t.Errorf("elfInterpreter(%s) = %q, want empty", path, got)
		}
	}
}

func TestCopyToolchainRecordsNodeLoader(t *testing.T) {
	prefix := writeToolchainFixture(t)
	useToolchainFixture(t, prefix, t.TempDir())
	sh, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	elfNode, err := os.ReadFile(sh)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "node"), elfNode, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := RuntimeToolchainDir()
	if err != nil || dir == "" {
		t.Fatalf("RuntimeToolchainDir() = (%q, %v)", dir, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".interp"))
	if want := elfInterpreter(sh) + "\n"; err != nil || string(got) != want || want == "\n" {
		t.Fatalf(".interp = (%q, %v), want %q", got, err, want)
	}
}
